package doctor

import (
	"testing"

	"github.com/ShizukaJiku/gitsi/internal/testhome"
)

// No test in this package touches the user's real profile.
func TestMain(m *testing.M) { testhome.Run(m) }
