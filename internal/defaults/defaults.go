// Package defaults holds site-specific fallback values for mus. The values
// here are baked into the binary at build time but are intentionally
// configurable via -ldflags so different labs can ship branded builds
// without forking the source.
//
// Resolution rule for every consumer:
//
//	value := envFromCascade          // .env walked up from cwd
//	if value == "" { value := defaults.X }   // baked-in fallback
//
// In other words: defaults are the LOWEST priority. Any `.env` value, any
// `mus config set` value, any explicit CLI flag wins over what's here.
//
// To override at build time:
//
//	go build -ldflags="\
//	  -X codeberg.org/mfiers/mus/internal/defaults.IRODSHome=/your/zone/home/lab \
//	  -X codeberg.org/mfiers/mus/internal/defaults.IRODSWeb=https://your-portal/data-object/view \
//	  -X codeberg.org/mfiers/mus/internal/defaults.IRODSPIDBase=https://your-portal/PID \
//	" ./cmd/mus
//
// Defaults are pre-set for the BADS lab on KU Leuven Mango — that's the
// repo's primary user. Other labs SHOULD override.
package defaults

var (
	// IRODSHome is the default iRODS collection mus's resolver uses when no
	// `irods_home` is in the .env cascade. BADS default.
	IRODSHome = "/gbiomed/home/BADS"

	// IRODSWeb is the default Mango "browse this path" URL prefix used to
	// stamp `irods_url` into sidecars. Path-based — does NOT survive moves;
	// see IRODSPIDBase for the persistent equivalent.
	IRODSWeb = "https://mango.kuleuven.be/data-object/view"

	// IRODSPIDBase is the default Mango persistent-URL prefix. The full PURL
	// is built as: <IRODSPIDBase>/<zone>/<catalog_id>/ — see Mango's
	// /PID/<zone>/<id>/ route in mango-portal. Survives object renames and
	// moves because the catalog ID is stable.
	IRODSPIDBase = "https://mango.kuleuven.be/PID"

	// IRODSGroup is the default iRODS user-group granted `own` permission
	// on every object mus uploads — so group members can read AND
	// overwrite each other's deposits. Override in mus.cfg with
	// `irods_group=<name>`. BADS default. Set to the empty string to
	// disable group-ACL stamping entirely (uploads then carry whatever
	// default ACL iRODS gives them — usually owner-only).
	IRODSGroup = "BADS"

	// ELNSampleLocation is the default eLabNext storage unit name that
	// mus-created samples (from `mus eln sample` and the auto-register
	// on `mus irods upload`) are placed into. Override via cascade
	// (`eln_sample_location=<name>` in mus.cfg — per-folder OR
	// ~/.config/mus/mus.cfg) or via the secret store
	// (`eln_sample_location`, next to eln_url). Empty string means "no
	// location" — samples are created without a storage placement.
	//
	// The value is a storage UNIT name (a freezer / cabinet / rack) as
	// visible in the ELN's Storage browser. mus resolves it to that
	// unit's root storageLayerID via GET /storage at batch start; every
	// sample ends up at that root compartment. If the named unit doesn't
	// exist on the tenant, mus warns once and continues WITHOUT a
	// location — the samples are still created, just unplaced.
	ELNSampleLocation = "BADS Mango"
)
