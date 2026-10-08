package main

import (
	"crypto/rand"
	"encoding/base64"
	"os"

	"github.com/swysgh/mill-box/log"

	"github.com/flynn/noise"
	"github.com/spf13/cobra"
)

func init() {
	commandGenerate.AddCommand(commandGenerateAnoissKeyPair)
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
