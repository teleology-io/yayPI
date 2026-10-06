package auth

import (
	"os"
	"testing"

	"github.com/rs/zerolog"
	"golang.org/x/crypto/bcrypt"
)

func TestMain(m *testing.M) {
	bcryptCost = bcrypt.MinCost // keep the suite fast; production cost is 12
	zerolog.SetGlobalLevel(zerolog.Disabled)
	os.Exit(m.Run())
}
