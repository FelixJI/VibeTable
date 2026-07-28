package workspacev2

import (
	"context"
	"errors"
	"io"
	"reflect"
	"sort"

	conflictresolution "github.com/vibetable/vibetable/sidecar/internal/conflict"
	"github.com/vibetable/vibetable/sidecar/internal/objectrepo"
)

type productionConflictDependencyScanner struct {
	repository objectrepo.Repository
}

func (scanner productionConflictDependencyScanner) ScanConflictDependencies(
	ctx context.Context,
	base conflictresolution.Candidate,
	local conflictresolution.Candidate,
	replica conflictresolution.Candidate,
) (conflictresolution.DependencyGraph, error) {
	if scanner.repository == nil {
		return conflictresolution.DependencyGraph{},
			conflictresolution.ErrDependencyIncomplete
	}
	union := map[string]map[string]struct{}{}
	for _, candidate := range []conflictresolution.Candidate{
		base, local, replica,
	} {
		for documentID := range candidate.Files {
			if documentID != "" && union[documentID] == nil {
				union[documentID] = map[string]struct{}{}
			}
		}
		for tableID := range candidate.Tables {
			if tableID != "" && union[tableID] == nil {
				union[tableID] = map[string]struct{}{}
			}
		}
		if candidate.Settings.ObjectID != "" &&
			union[conflictresolution.WorkspaceSettingsItemID] == nil {
			union[conflictresolution.WorkspaceSettingsItemID] =
				map[string]struct{}{}
		}
		projection, err := scanner.project(ctx, candidate)
		if err != nil {
			return conflictresolution.DependencyGraph{}, err
		}
		for source, targets := range projection.Edges {
			if union[source] == nil {
				union[source] = map[string]struct{}{}
			}
			for _, target := range targets {
				if target == "" || target == source {
					continue
				}
				if union[target] == nil {
					union[target] = map[string]struct{}{}
				}
				// Relation/view/automation/plugin closure is intentionally
				// bidirectional: replacing a referenced table can invalidate
				// its dependents just as replacing a dependent can dangle its
				// references.
				union[source][target] = struct{}{}
				union[target][source] = struct{}{}
			}
		}
	}
	edges := make(map[string][]string, len(union))
	for source, targets := range union {
		edges[source] = make([]string, 0, len(targets))
		for target := range targets {
			edges[source] = append(edges[source], target)
		}
		sort.Strings(edges[source])
	}
	return conflictresolution.DependencyGraph{
		Complete: true,
		Edges:    edges,
	}, nil
}

func (scanner productionConflictDependencyScanner) project(
	ctx context.Context,
	candidate conflictresolution.Candidate,
) (conflictresolution.SQLiteProjection, error) {
	if candidate.BusinessDatabaseObjectID == "" ||
		candidate.Settings.ObjectID == "" {
		return conflictresolution.SQLiteProjection{},
			conflictresolution.ErrDependencyIncomplete
	}
	database, err := readConflictObject(
		ctx,
		scanner.repository,
		objectrepo.ObjectID(candidate.BusinessDatabaseObjectID),
	)
	if err != nil {
		return conflictresolution.SQLiteProjection{}, err
	}
	_, err = readConflictObject(
		ctx,
		scanner.repository,
		objectrepo.ObjectID(candidate.Settings.ObjectID),
	)
	if err != nil {
		return conflictresolution.SQLiteProjection{}, err
	}
	projection, err := conflictresolution.ProjectSQLiteDatabase(
		ctx,
		database,
		candidate.BusinessDatabaseObjectID,
		candidate.AttachmentObjects,
	)
	if err != nil {
		return conflictresolution.SQLiteProjection{}, err
	}
	if !reflect.DeepEqual(projection.Tables, candidate.Tables) {
		return conflictresolution.SQLiteProjection{},
			errors.New("conflict.candidate_projection_mismatch")
	}
	return projection, nil
}

func readConflictObject(
	ctx context.Context,
	repository objectrepo.Repository,
	id objectrepo.ObjectID,
) ([]byte, error) {
	if repository == nil || id == "" {
		return nil, errors.New("conflict.object_required")
	}
	reader, err := repository.Open(ctx, id)
	if err != nil {
		return nil, err
	}
	content, readErr := io.ReadAll(io.LimitReader(reader, 512<<20))
	closeErr := reader.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		return nil, err
	}
	return content, nil
}
