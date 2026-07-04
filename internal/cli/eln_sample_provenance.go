package cli

// Provenance auto-fill for ELN sample records.
//
// When `mus eln sample` or `mus irods upload` creates a Sample on
// eLabNext, mus fills any of the following meta fields that the
// tenant has DECLARED on the sample type:
//
//   path                the file's location (local abs for `eln sample`,
//                       iRODS path for `irods upload`)
//   sha256sum, sha256   file's sha256 hex
//   size_bytes, size    file size in bytes (decimal string)
//   mtime               file mtime ISO-8601
//   upload_data,        upload timestamp (RFC3339 UTC)
//   upload_date
//   upload_user         os user (Username / uid.String() fallback)
//   upload_from_server  os.Hostname()
//   upload_from_path    absolute LOCAL path (== path for eln sample,
//                       distinct from path for irods upload)
//   purl                iRODS PURL (only populated when known)
//   kind                "file" / "folder" / "archive"
//   mus_version         cli.Version at upload time
//
// Fields the tenant has NOT declared are silently omitted. mus never
// creates ad-hoc meta fields (POST /samples/{id}/meta with a blank
// sampleTypeMetaID) — the eLabNext docs mark those as unsearchable,
// and we want every field to be searchable in the ELN inventory.

import (
	"fmt"
	"io"
	"os"
	"os/user"
	"strconv"
	"strings"
	"time"

	"codeberg.org/mfiers/mus/internal/config"
	"codeberg.org/mfiers/mus/internal/eln"
	"codeberg.org/mfiers/mus/internal/secret"
)

// sampleProvenance is the raw key→value dict of everything mus knows
// about a file at sample-creation time. Only the keys the sample type
// actually declares end up in the sampleMetas payload; everything else
// is discarded. Callers build one of these per file, then pipe it
// through metasForType.
type sampleProvenance struct {
	// path is the value for the "path" meta field. Callers set this
	// to the local abs path (eln sample) or the iRODS path (irods
	// upload). uploadFromPath is always the local abs path — for
	// irods uploads the two differ.
	path           string
	uploadFromPath string
	sha256         string
	sizeBytes      int64
	mtime          time.Time
	uploadTime     time.Time
	// purl is optional — only set when we already know the iRODS
	// persistent URL (post-upload flow). Empty for `mus eln sample`.
	purl string
	// kind is "file" (default) / "folder" / "archive". Empty →
	// treated as "file".
	kind string
}

// buildProvenanceDict expands the struct into the case-insensitive
// key→value dict used by metasForType. Empty values are OK — they'll
// be dropped by the intersection step so the ELN doesn't get blanks.
func buildProvenanceDict(p sampleProvenance) map[string]string {
	who := ""
	if u, err := user.Current(); err == nil {
		if u.Username != "" {
			who = u.Username
		} else if u.Uid != "" {
			who = u.Uid
		}
	}
	host, _ := os.Hostname()
	kind := p.kind
	if kind == "" {
		kind = "file"
	}

	upload := p.uploadTime
	if upload.IsZero() {
		upload = time.Now().UTC().Truncate(time.Second)
	}

	dict := map[string]string{
		"path":               p.path,
		"upload_from_path":   p.uploadFromPath,
		"sha256sum":          p.sha256,
		"sha256":             p.sha256,
		"size_bytes":         strconv.FormatInt(p.sizeBytes, 10),
		"size":               strconv.FormatInt(p.sizeBytes, 10),
		"mtime":              "",
		"upload_data":        upload.Format(time.RFC3339),
		"upload_date":        upload.Format(time.RFC3339),
		"upload_user":        who,
		"upload_from_server": host,
		"purl":               p.purl,
		"kind":               kind,
		"mus_version":        Version,
	}
	if !p.mtime.IsZero() {
		dict["mtime"] = p.mtime.UTC().Format(time.RFC3339)
	}
	return dict
}

// metasForType intersects the provenance dict with the tenant's
// declared meta fields on this sample type. Returns a SampleMetaNew
// slice ready to pass as CreateSampleReq.SampleMetas.
//
// Matching is case-insensitive on the meta key ("Path" and "path"
// both hit "path"). Fields with an empty value are dropped — no point
// stamping the sample with empty strings that later reads would treat
// as "unset".
//
// Tenants can also declare fields mus doesn't know about (e.g. a
// custom "aliquot_of" pointer). Those pass through untouched — mus
// simply doesn't fill them.
func metasForType(prov map[string]string, typeMetas []eln.SampleTypeMeta) []eln.SampleMetaNew {
	if len(typeMetas) == 0 {
		return nil
	}
	out := make([]eln.SampleMetaNew, 0, len(typeMetas))
	for _, tm := range typeMetas {
		val, ok := prov[strings.ToLower(strings.TrimSpace(tm.Key))]
		if !ok || val == "" {
			continue
		}
		dt := tm.SampleDataType
		if dt == "" {
			dt = "TEXT"
		}
		out = append(out, eln.SampleMetaNew{
			Key:            tm.Key,
			SampleDataType: dt,
			Value:          val,
		})
	}
	return out
}

// resolveSampleLocationName picks the storage-unit NAME for mus-created
// samples in this batch. Resolution order (first non-empty wins):
//
//  1. eln_sample_location in the mus.cfg cascade — per-folder override
//     takes precedence over ~/.config/mus/mus.cfg's global setting.
//  2. eln_sample_location in the secret store — user-level default
//     (sits next to eln_url / eln_apikey).
//
// If nothing is set at either level, returns the empty string —
// callers then create samples WITHOUT a storage-layer placement.
// mus deliberately has no compile-time default here: labs that want
// mus-created samples placed must opt in explicitly.
func resolveSampleLocationName(env *config.Env, store secret.Store) string {
	if env != nil {
		if v := strings.TrimSpace(env.String("eln_sample_location")); v != "" {
			return v
		}
	}
	if store != nil {
		if v, err := store.Get("eln_sample_location"); err == nil {
			if s := strings.TrimSpace(v); s != "" {
				return s
			}
		}
	}
	return ""
}

// resolveSampleStorageLayerID resolves the configured location name to
// a storageLayerID (the root layer of the named storage unit). When
// nothing is configured, returns 0 silently — no lookup, no warning.
// When a name IS configured but lookup fails, returns 0 AND warns to
// stderr so the depositor sees the typo. Never returns an error —
// sample creation should proceed regardless.
func resolveSampleStorageLayerID(client *eln.Client, env *config.Env, store secret.Store, stderr io.Writer) int64 {
	name := resolveSampleLocationName(env, store)
	if name == "" {
		// Not configured anywhere → deliberate silence. mus creates
		// samples without a placement; ELN shows them in the flat
		// inventory listing.
		return 0
	}
	unit, err := client.FindStorageByName(name)
	if err != nil {
		fmt.Fprintf(stderr,
			"  ⚠ ELN storage unit %q not found — samples will be created without a location: %v\n",
			name, err)
		return 0
	}
	fmt.Fprintf(stderr, "  storage: %s (storageID=%d, root layer=%d)\n",
		unit.Name, unit.StorageID, unit.StorageLayer.StorageLayerID)
	return unit.StorageLayer.StorageLayerID
}

