package h5ad

import (
	"context"
	"testing"
)

func TestObsValues_RejectsEmptyArgs(t *testing.T) {
	ctx := context.Background()
	if _, err := ObsValues(ctx, "", "leiden", nil); err == nil {
		t.Error("expected error for empty h5ad path")
	}
	if _, err := ObsValues(ctx, "/path/x.h5ad", "", nil); err == nil {
		t.Error("expected error for empty obs column name")
	}
}

func TestErrObsColumnMissing_Error(t *testing.T) {
	e := &ErrObsColumnMissing{
		Column: "missing_col",
		H5ad:   "/path/x.h5ad",
		Detail: "obs column 'missing_col' not found in /path/x.h5ad; available columns: ['leiden', 'sample']",
	}
	if e.Error() != e.Detail {
		t.Errorf("Error() should return Detail; got %q", e.Error())
	}
}
