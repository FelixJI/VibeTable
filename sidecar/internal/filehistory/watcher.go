package filehistory

import (
	"bytes"
	"context"
	"errors"
	"io"
	"mime"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/vibetable/vibetable/sidecar/internal/writecoordinator"
)

const (
	defaultWatcherMaxFileSize = int64(512 << 20)
	watcherDebounce           = 250 * time.Millisecond
	watcherStabilityDelay     = 75 * time.Millisecond
	watcherPeriodicRescan     = 30 * time.Second
)

type WatchEvent struct {
	Path                string
	Confirmation        *IdentityConfirmation
	ObservedContentHash string
	ObservedSize        int64
	Missing             bool
	Err                 error
	Mutated             bool
}

type Watcher struct {
	filesRoot string
	ingestor  *Ingestor
	token     func() writecoordinator.Token
	maxSize   int64
	onEvent   func(WatchEvent)

	mu      sync.Mutex
	cancel  context.CancelFunc
	done    chan struct{}
	started bool
}

func NewWatcher(
	filesRoot string,
	ingestor *Ingestor,
	token func() writecoordinator.Token,
	onEvent func(WatchEvent),
) (*Watcher, error) {
	if ingestor == nil || token == nil {
		return nil, errors.New("filehistory.watcher_dependencies_required")
	}
	root, err := cleanAbsoluteRoot(filesRoot)
	if err != nil {
		return nil, err
	}
	if err := ensureSafeDirectory(root); err != nil {
		return nil, err
	}
	return &Watcher{
		filesRoot: root,
		ingestor:  ingestor,
		token:     token,
		maxSize:   defaultWatcherMaxFileSize,
		onEvent:   onEvent,
	}, nil
}

// Start performs the initial reconciliation synchronously, then supervises an
// fsnotify subscription. Every subscription recreation is followed by another
// full reconciliation so missed events cannot silently disappear.
func (watcher *Watcher) Start(ctx context.Context) error {
	if watcher == nil {
		return errors.New("filehistory.watcher_required")
	}
	watcher.mu.Lock()
	if watcher.started {
		watcher.mu.Unlock()
		return errors.New("filehistory.watcher_already_started")
	}
	runContext, cancel := context.WithCancel(context.Background())
	watcher.cancel = cancel
	watcher.done = make(chan struct{})
	watcher.started = true
	watcher.mu.Unlock()
	if err := watcher.Rescan(ctx); err != nil {
		cancel()
		close(watcher.done)
		return err
	}
	go watcher.run(runContext)
	return nil
}

func (watcher *Watcher) Close() error {
	if watcher == nil {
		return nil
	}
	watcher.mu.Lock()
	if !watcher.started {
		watcher.mu.Unlock()
		return nil
	}
	cancel := watcher.cancel
	done := watcher.done
	watcher.started = false
	watcher.mu.Unlock()
	cancel()
	<-done
	return nil
}

func (watcher *Watcher) run(ctx context.Context) {
	defer close(watcher.done)
	for ctx.Err() == nil {
		err := watcher.runSubscription(ctx)
		if ctx.Err() != nil {
			return
		}
		watcher.emit(WatchEvent{Err: err})
		timer := time.NewTimer(watcherDebounce)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		if err := watcher.Rescan(ctx); err != nil && ctx.Err() == nil {
			watcher.emit(WatchEvent{Err: err})
		}
	}
}

func (watcher *Watcher) runSubscription(ctx context.Context) error {
	subscription, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	defer subscription.Close()
	if err := watcher.addDirectories(subscription); err != nil {
		return err
	}
	debounce := time.NewTimer(time.Hour)
	if !debounce.Stop() {
		<-debounce.C
	}
	defer debounce.Stop()
	periodic := time.NewTicker(watcherPeriodicRescan)
	defer periodic.Stop()
	pending := false
	for {
		select {
		case <-ctx.Done():
			return nil
		case event, ok := <-subscription.Events:
			if !ok {
				return errors.New("filehistory.watcher_events_closed")
			}
			if event.Op&fsnotify.Create != 0 {
				if info, statErr := os.Lstat(event.Name); statErr == nil &&
					info.IsDir() && !pathHasReparsePoint(event.Name) {
					_ = watcher.addDirectoryTree(subscription, event.Name)
				}
			}
			pending = true
			debounce.Reset(watcherDebounce)
		case err, ok := <-subscription.Errors:
			if !ok {
				return errors.New("filehistory.watcher_errors_closed")
			}
			return err
		case <-debounce.C:
			if pending {
				pending = false
				if err := watcher.Rescan(ctx); err != nil {
					watcher.emit(WatchEvent{Err: err})
				}
			}
		case <-periodic.C:
			if err := watcher.Rescan(ctx); err != nil {
				watcher.emit(WatchEvent{Err: err})
			}
		}
	}
}

func (watcher *Watcher) addDirectories(
	subscription *fsnotify.Watcher,
) error {
	return watcher.addDirectoryTree(subscription, watcher.filesRoot)
}

func (watcher *Watcher) addDirectoryTree(
	subscription *fsnotify.Watcher,
	root string,
) error {
	return filepath.WalkDir(root, func(
		path string,
		entry os.DirEntry,
		err error,
	) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() {
			return nil
		}
		if pathHasReparsePoint(path) {
			if path == root {
				return ErrUnsafeFilePath
			}
			return filepath.SkipDir
		}
		return subscription.Add(path)
	})
}

func (watcher *Watcher) Rescan(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	onDisk, err := watcher.scanStableFiles(ctx)
	if err != nil {
		return err
	}
	documents := watcher.ingestor.service.List()
	activeByPath := make(map[string]Document, len(documents))
	for _, document := range documents {
		if document.Status == DocumentActive {
			key, keyErr := windowsPathKey(document.RelativePath)
			if keyErr != nil {
				return keyErr
			}
			if _, exists := activeByPath[key]; exists {
				return ErrPathConflict
			}
			activeByPath[key] = document
		}
	}
	paths := make([]string, 0, len(onDisk))
	for relative := range onDisk {
		paths = append(paths, relative)
	}
	sort.Strings(paths)
	token := watcher.token()
	for _, relative := range paths {
		content := onDisk[relative]
		key, keyErr := windowsPathKey(relative)
		if keyErr != nil {
			return keyErr
		}
		document, tracked := activeByPath[key]
		if tracked {
			delete(activeByPath, key)
			effective := revisionByID(
				document, document.EffectiveRevisionID,
			)
			if effective != nil &&
				effective.ContentHash == contentHash(content) &&
				effective.Size == int64(len(content)) {
				continue
			}
		}
		mimeType := mime.TypeByExtension(filepath.Ext(relative))
		if mimeType == "" {
			mimeType = "application/octet-stream"
		}
		change := ExternalChange{
			Token: token, Kind: ExternalStableSave,
			TargetPath: relative,
			Content:    content, ContentProvided: true,
			RevisionKind: RevisionAutosave,
			MimeType:     mimeType,
			CreatedBy:    "workspace-file-watcher",
			DeviceID:     token.ClaimID,
			Comment:      "Ingested from files/",
		}
		if tracked {
			change.DocumentID = document.DocumentID
			change.ExpectedEffectiveRevision =
				stringPointer(document.EffectiveRevisionID)
		}
		result, err := watcher.ingestor.Ingest(ctx, change)
		if err != nil {
			watcher.emit(WatchEvent{Path: relative, Err: err})
			continue
		}
		if result.Confirmation != nil {
			watcher.emit(WatchEvent{
				Path: relative, Confirmation: result.Confirmation,
				ObservedContentHash: contentHash(content),
				ObservedSize:        int64(len(content)),
			})
		} else if (result.Save != nil && !result.Save.NoOp) ||
			(result.Mutation != nil && !result.Mutation.NoOp) {
			watcher.emit(WatchEvent{Path: relative, Mutated: true})
		}
	}
	missing := make([]Document, 0, len(activeByPath))
	for _, document := range activeByPath {
		missing = append(missing, document)
	}
	sort.Slice(missing, func(left, right int) bool {
		return missing[left].RelativePath < missing[right].RelativePath
	})
	for _, document := range missing {
		watcher.emit(WatchEvent{
			Path:    document.RelativePath,
			Missing: true,
			Confirmation: &IdentityConfirmation{
				Reason: "tracked file is missing; delete, rename, or move " +
					"requires confirmation",
				CandidateDocumentIDs: []string{document.DocumentID},
			},
		})
	}
	return nil
}

func (watcher *Watcher) ReadStable(
	ctx context.Context,
	relative string,
) ([]byte, error) {
	if watcher == nil {
		return nil, errors.New("filehistory.watcher_required")
	}
	normalized, err := normalizePath(relative)
	if err != nil || normalized != relative {
		return nil, ErrUnsafeFilePath
	}
	target := filepath.Join(
		watcher.filesRoot,
		filepath.FromSlash(normalized),
	)
	if !pathWithin(watcher.filesRoot, target) ||
		pathHasReparsePoint(target) {
		return nil, ErrUnsafeFilePath
	}
	return watcher.stableRead(ctx, target)
}

func (watcher *Watcher) scanStableFiles(
	ctx context.Context,
) (map[string][]byte, error) {
	result := map[string][]byte{}
	err := filepath.WalkDir(watcher.filesRoot, func(
		path string,
		entry os.DirEntry,
		walkErr error,
	) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if path == watcher.filesRoot {
			return nil
		}
		if pathHasReparsePoint(path) {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			watcher.emit(WatchEvent{
				Path: path, Err: ErrUnsafeFilePath,
			})
			return nil
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			watcher.emit(WatchEvent{
				Path: path, Err: ErrUnsafeFilePath,
			})
			return nil
		}
		if info.Size() < 0 || info.Size() > watcher.maxSize {
			watcher.emit(WatchEvent{
				Path: path,
				Err:  errors.New("filehistory.watcher_file_too_large"),
			})
			return nil
		}
		relative, err := filepath.Rel(watcher.filesRoot, path)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		if normalized, normalizeErr := normalizePath(relative); normalizeErr != nil || normalized != relative {
			watcher.emit(WatchEvent{
				Path: relative, Err: ErrUnsafeFilePath,
			})
			return nil
		}
		content, err := watcher.stableRead(ctx, path)
		if err != nil {
			watcher.emit(WatchEvent{Path: relative, Err: err})
			return nil
		}
		result[relative] = content
		return nil
	})
	return result, err
}

func (watcher *Watcher) stableRead(
	ctx context.Context,
	path string,
) ([]byte, error) {
	for attempt := 0; attempt < 3; attempt++ {
		before, err := os.Stat(path)
		if err != nil || !before.Mode().IsRegular() ||
			before.Size() < 0 || before.Size() > watcher.maxSize {
			return nil, errors.Join(
				errors.New("filehistory.watcher_unstable_file"),
				err,
			)
		}
		file, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		content, readErr := io.ReadAll(io.LimitReader(file, watcher.maxSize+1))
		closeErr := file.Close()
		if err := errors.Join(readErr, closeErr); err != nil {
			return nil, err
		}
		if int64(len(content)) > watcher.maxSize {
			return nil, errors.New("filehistory.watcher_file_too_large")
		}
		after, err := os.Stat(path)
		if err == nil &&
			before.Size() == after.Size() &&
			before.ModTime() == after.ModTime() {
			return bytes.Clone(content), nil
		}
		timer := time.NewTimer(watcherStabilityDelay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
	return nil, errors.New("filehistory.watcher_unstable_file")
}

func (watcher *Watcher) emit(event WatchEvent) {
	if watcher.onEvent != nil {
		watcher.onEvent(event)
	}
}

func watcherPath(relative string) string {
	return strings.TrimSpace(filepath.ToSlash(relative))
}
