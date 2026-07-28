package snapshotpkg

import (
	"errors"
	"io"

	"filippo.io/age"
)

type AgeNative struct{}

func closeAgeWriter(output io.Writer, recipient age.Recipient, input io.Reader) error {
	encrypted, err := age.Encrypt(output, recipient)
	if err != nil {
		return err
	}
	if _, err := io.Copy(encrypted, input); err != nil {
		return err
	}
	return encrypted.Close()
}

func (AgeNative) Encrypt(recipientText string, input io.Reader, output io.Writer) error {
	return (AgeNative{}).EncryptRecipients(
		[]string{recipientText}, input, output,
	)
}

func (AgeNative) EncryptRecipients(
	recipientTexts []string,
	input io.Reader,
	output io.Writer,
) error {
	if len(recipientTexts) == 0 {
		return ErrInvalidPackage
	}
	recipients := make([]age.Recipient, 0, len(recipientTexts))
	for _, recipientText := range recipientTexts {
		recipient, err := age.ParseX25519Recipient(recipientText)
		if err != nil {
			return errors.Join(ErrInvalidPackage, err)
		}
		recipients = append(recipients, recipient)
	}
	encrypted, err := age.Encrypt(output, recipients...)
	if err != nil {
		return err
	}
	if _, err := io.Copy(encrypted, input); err != nil {
		return err
	}
	return encrypted.Close()
}

func (AgeNative) EncryptPassphrase(passphrase string, input io.Reader, output io.Writer) error {
	if passphrase == "" {
		return ErrInvalidPackage
	}
	recipient, err := age.NewScryptRecipient(passphrase)
	if err != nil {
		return errors.Join(ErrInvalidPackage, err)
	}
	return closeAgeWriter(output, recipient, input)
}

func (AgeNative) Decrypt(identityText string, input io.Reader, output io.Writer) error {
	identity, err := age.ParseX25519Identity(identityText)
	if err != nil {
		return errors.Join(ErrInvalidPackage, err)
	}
	decrypted, err := age.Decrypt(input, identity)
	if err != nil {
		return errors.Join(ErrInvalidPackage, err)
	}
	_, err = io.Copy(output, decrypted)
	return err
}

func (AgeNative) DecryptPassphrase(passphrase string, input io.Reader, output io.Writer) error {
	if passphrase == "" {
		return ErrInvalidPackage
	}
	identity, err := age.NewScryptIdentity(passphrase)
	if err != nil {
		return errors.Join(ErrInvalidPackage, err)
	}
	decrypted, err := age.Decrypt(input, identity)
	if err != nil {
		return errors.Join(ErrInvalidPackage, err)
	}
	_, err = io.Copy(output, decrypted)
	return err
}

func GenerateAgeIdentity() (identity string, recipient string, err error) {
	value, err := age.GenerateX25519Identity()
	if err != nil {
		return "", "", err
	}
	return value.String(), value.Recipient().String(), nil
}
