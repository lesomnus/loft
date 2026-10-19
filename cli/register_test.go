package cli_test

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lesomnus/xli"

	"github.com/lesomnus/rove/cli"
	"github.com/lesomnus/rove/internal/ent/asset"
)

// tried is [run] for a command that may refuse: what it said, and the error.
func tried(t *testing.T, k *xli.Command, args ...string) (string, error) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	was := os.Stdout
	os.Stdout = w
	err = k.Run(t.Context(), args)
	os.Stdout = was
	w.Close()
	b, _ := io.ReadAll(r)
	return string(b), err
}

// TestASheetIsRegisteredFromATerminal: `rove register` writes the form,
// shows what a sheet in it would register -- line by line, with the tags it
// would give and the models it would make -- and registers it only with
// `--yes`, as one intake. Sent again, its tags are taken, and it says where.
func TestASheetIsRegisteredFromATerminal(t *testing.T) {
	s := serve(t)
	x := s.x
	s.signIn("owner", "owner1234")
	dir := t.TempDir()
	count := func() int {
		n, err := s.s.Ent.Asset.Query().Where(asset.DateErasedIsNil()).Count(t.Context())
		x.NoError(err)
		return n
	}

	form := filepath.Join(dir, "form.csv")
	x.Contains(run(t, cli.NewCmdRegister(), "--form", form), "대량 등록 양식")
	b, err := os.ReadFile(form)
	x.NoError(err)
	x.True(strings.HasPrefix(strings.TrimPrefix(string(b), "\ufeff"), "이름,종류,유형"), "%s", b)

	sheet := filepath.Join(dir, "intake.csv")
	x.NoError(os.WriteFile(sheet, []byte("이름,태그,모델,제조사\n노트북,NB-1,X1,Lenovo\n모니터,,,\n"), 0o644))
	before := count()

	out := run(t, cli.NewCmdRegister(), sheet)
	x.Contains(out, "2행  NB-1")
	x.Contains(out, "3행  A-00001")
	x.Contains(out, "새로 만들 모델: Lenovo X1")
	x.Contains(out, "--yes")
	x.Equal(before, count(), "shown, and nothing registered")

	out = run(t, cli.NewCmdRegister(), "--yes", "--reason", "10월 입고", sheet)
	x.Contains(out, "2개를 등록했습니다")
	x.Equal(before+2, count())

	out, err = tried(t, cli.NewCmdRegister(), "--yes", sheet)
	x.ErrorContains(err, "문제가 1건")
	x.Contains(out, "2행 태그: 태그 NB-1는 이미 있는 자산의 것입니다")
	x.Equal(before+2, count(), "nothing registered the second time")

	s.signIn("kim", "kim-password")
	_, err = tried(t, cli.NewCmdRegister(), sheet)
	x.ErrorContains(err, "PermissionDenied", "a member registers nothing")
}
