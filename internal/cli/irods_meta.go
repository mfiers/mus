package cli

// applyMetadataToIRODS — after a successful iRODS upload, attach a curated
// subset of sidecar fields to the object via `iron meta set` so the data
// stays self-describing even if the local sidecar is deleted or moved.
//
// Keys are flat, prefixed with `mus_` to avoid colliding with other tools'
// AVU schemas (e.g. KU Leuven's mango_mdschema). The same key set works for
// both file and folder/archive objects.
//
// One `iron meta set` per key is spawned — that's ~6 process invocations
// per upload. Acceptable for now; could be batched into a single iron call
// later if iron grows that surface.

import (
	"context"
	"fmt"
	"io"
	"strconv"

	"codeberg.org/mfiers/mus/internal/iron"
	"codeberg.org/mfiers/mus/internal/sidecar"
)

// applyMetadataToIRODS writes selected sidecar fields onto the iRODS object
// at remotePath. Returns the first hard error; per-key failures are logged
// to stderr but don't abort (metadata is bonus, not load-bearing).
func applyMetadataToIRODS(
	ctx context.Context,
	client *iron.Client,
	remotePath string,
	doc *sidecar.Doc,
	stderr io.Writer,
) {
	type pair struct{ key, value string }
	var kv []pair

	if doc.DataProject != "" {
		kv = append(kv, pair{"mus_data_project", doc.DataProject})
	}
	if doc.ELN != nil {
		if doc.ELN.ExperimentID != "" {
			kv = append(kv, pair{"mus_eln_experiment_id", doc.ELN.ExperimentID})
		}
		if doc.ELN.ExperimentName != "" {
			kv = append(kv, pair{"mus_eln_experiment_name", doc.ELN.ExperimentName})
		}
		if doc.ELN.ProjectName != "" {
			kv = append(kv, pair{"mus_eln_project_name", doc.ELN.ProjectName})
		}
		if doc.ELN.StudyName != "" {
			kv = append(kv, pair{"mus_eln_study_name", doc.ELN.StudyName})
		}
	}
	if doc.File.Sha256 != "" {
		kv = append(kv, pair{"mus_sha256", doc.File.Sha256})
	}
	if doc.Folder != nil && doc.Folder.RecursiveSha256 != "" {
		kv = append(kv, pair{"mus_recursive_sha256", doc.Folder.RecursiveSha256})
	}
	if doc.Folder != nil && doc.Folder.FileCount > 0 {
		kv = append(kv, pair{"mus_file_count",
			strconv.FormatInt(doc.Folder.FileCount, 10)})
	}
	if doc.Archive != nil {
		if doc.Archive.Format != "" {
			kv = append(kv, pair{"mus_archive_format", doc.Archive.Format})
		}
		if doc.Archive.Sha256 != "" {
			kv = append(kv, pair{"mus_archive_sha256", doc.Archive.Sha256})
		}
	}
	if doc.IRODS != nil && !doc.IRODS.UploadedAt.IsZero() {
		kv = append(kv, pair{"mus_uploaded_at", doc.IRODS.UploadedAt.UTC().Format("2006-01-02T15:04:05Z")})
	}

	for _, p := range kv {
		if err := client.MetaSet(ctx, remotePath, p.key, p.value); err != nil {
			fmt.Fprintf(stderr, "  WARNING: iron meta set %s=%s failed: %v\n",
				p.key, p.value, err)
		}
	}
}
