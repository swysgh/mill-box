package main

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"os"

	"github.com/swysgh/mill-box/log"

	"github.com/flynn/noise"
	"github.com/spf13/cobra"
	"golang.org/x/crypto/curve25519"
)

func init() {
	commandGenerate.AddCommand(commandGenerateAnoissKeyPair)
	commandGenerate.AddCommand(commandDeriveAnoissPublicKey)
}

var commandGenerateAnoissKeyPair = &cobra.Command{
	Use:   "anoiss-keypair",
	Short: "Generate anoiss Noise key pair",
	Args:  cobra.NoArgs,
	Run: func(cmd *cobra.Command, args []string) {
		err := generateAnoissKey()
		if err != nil {
			log.Fatal(err)
		}
	},
}

func generateAnoissKey() error {
	kp, err := noise.DH25519.GenerateKeypair(rand.Reader)
	if err != nil {
		return err
	}
	os.Stdout.WriteString("PrivateKey: " + base64.RawURLEncoding.EncodeToString(kp.Private) + "\n")
	os.Stdout.WriteString("PublicKey: " + base64.RawURLEncoding.EncodeToString(kp.Public) + "\n")
	return nil
}

var commandDeriveAnoissPublicKey = &cobra.Command{
	Use:   "anoiss-pubkey <private_key>",
	Short: "Derive the anoiss public key from a private key",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		err := deriveAnoissPublicKey(args[0])
		if err != nil {
			log.Fatal(err)
		}
	},
}

// deriveAnoissPublicKey derives the X25519 public key (priv * Basepoint) from a
// base64 private key. This is exactly how GenerateKeypair computed the public
// key, so the result is byte-identical to the one that was originally printed.
func deriveAnoissPublicKey(encoded string) error {
	// Same decoding order as protocol/anoiss decodeKey: RawURL first, then Std
	priv, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		priv, err = base64.StdEncoding.DecodeString(encoded)
	}
	if err != nil {
		return fmt.Errorf("anoiss: private key: invalid base64 encoding")
	}
	if len(priv) != 32 {
		return fmt.Errorf("anoiss: private key: expected 32 bytes, got %d", len(priv))
	}
	pub, err := curve25519.X25519(priv, curve25519.Basepoint)
	if err != nil {
		return fmt.Errorf("anoiss: private key: %s", err)
	}
	os.Stdout.WriteString(base64.RawURLEncoding.EncodeToString(pub) + "\n")
	return nil
}
