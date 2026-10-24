package policy

import (
	"testing"

	"github.com/teleology-io/yayPI/internal/middleware"
)

func TestConditionsAcceptBothQuoteStyles(t *testing.T) {
	sub := &middleware.Subject{Role: "admin"}
	for _, c := range []string{`subject.role == "admin"`, `subject.role == 'admin'`, `subject.role == admin`, `subject.role in ['user', 'admin']`} {
		if ok, err := MatchCondition(c, sub); err != nil || !ok {
			t.Errorf("%s: %v %v", c, ok, err)
		}
	}
	if ok, _ := MatchCondition(`subject.role == 'user'`, sub); ok {
		t.Error("single-quoted mismatch matched")
	}
}
