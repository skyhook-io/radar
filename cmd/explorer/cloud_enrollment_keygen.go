package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
)

func cloudEnrollmentKeygen(args []string, out, errOut io.Writer) int {
	fs := flag.NewFlagSet("radar cloud enrollment-keygen", flag.ContinueOnError)
	fs.SetOutput(errOut)
	path := fs.String("out", "", "Private-key file to create; the public key is written to FILE.pub")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if *path == "" || fs.NArg() != 0 {
		fmt.Fprintln(errOut, "Use radar cloud enrollment-keygen --out FILE. Both FILE and FILE.pub must be new files.")
		return 2
	}
	if err := writeEnrollmentKeyPair(*path); err != nil {
		fmt.Fprintf(errOut, "Could not create enrollment key pair: %v\n", err)
		return 1
	}
	fmt.Fprintf(out, "Private key: %s\nPublic key: %s.pub\nRegister only the public file with Hub. Keep the private file in your cluster's enrollment Secret.\n", *path, *path)
	return 0
}

func writeEnrollmentKeyPair(path string) error {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return errors.New("could not generate an Ed25519 key")
	}
	privateDER, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		return err
	}
	publicDER, err := x509.MarshalPKIXPublicKey(publicKey)
	if err != nil {
		return err
	}
	privateFile, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("private file was not created: %w", err)
	}
	publicFile, err := os.OpenFile(path+".pub", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		_ = privateFile.Close()
		_ = os.Remove(path)
		return fmt.Errorf("public file was not created: %w", err)
	}
	complete := false
	defer func() {
		if !complete {
			_ = os.Remove(path)
			_ = os.Remove(path + ".pub")
		}
	}()
	privateErr := pem.Encode(privateFile, &pem.Block{Type: "PRIVATE KEY", Bytes: privateDER})
	publicErr := pem.Encode(publicFile, &pem.Block{Type: "PUBLIC KEY", Bytes: publicDER})
	privateCloseErr := privateFile.Close()
	publicCloseErr := publicFile.Close()
	if err := errors.Join(privateErr, publicErr, privateCloseErr, publicCloseErr); err != nil {
		return errors.New("key files could not be saved completely")
	}
	complete = true
	return nil
}
