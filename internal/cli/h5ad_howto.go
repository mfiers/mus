package cli

// `mus h5ad howto` — print a condensed depositor primer.
//
// The full spec at /data/1/users/mark/anthive4/doc/H5AD_INGESTION_SPEC.md
// is depositor-detailed (~250 lines). The howto here is a 1-page
// summary suitable for handing to another human OR feeding to
// another Claude session as system context.
//
// Body lives in a Go raw string so it travels with the mus binary.
// We avoid backticks INSIDE the text (raw strings can't contain
// them) — code markers in the rendered output use single quotes
// instead, which read fine in plain terminal output.
// Keep in sync with the spec when hard fields change.

import (
	"github.com/spf13/cobra"
)

func newH5adHowtoCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "howto",
		Short: "print a depositor primer for h5ad annotation",
		Long: "Emits a condensed how-to guide for annotating an h5ad file so it\n" +
			"passes `mus h5ad check` and the downstream catalog (anthive) ingest.\n" +
			"Plain text on stdout — suitable for piping to less, mail, an LLM\n" +
			"prompt, or another Claude session for context.\n\n" +
			"For the full spec (structural h5ad requirements, deeper rationale,\n" +
			"common failure modes), see the source doc in the anthive repo at\n" +
			"doc/H5AD_INGESTION_SPEC.md.",
		RunE: func(cmd *cobra.Command, args []string) error {
			_, err := cmd.OutOrStdout().Write([]byte(h5adHowtoText))
			return err
		},
	}
}

const h5adHowtoText = `Annotating an .h5ad file for deposit (mus + anthive)
=====================================================

This is a depositor-side primer. Run 'mus h5ad check FILE' to validate;
the catalog (anthive) accepts only files that pass it.

1. STRUCTURAL REQUIREMENTS (inside the .h5ad)
   -------------------------------------------
   These are checked mechanically. No manual override.

   - adata.X         → log-normalized expression. float32/float64.
                       Minimum value MUST be 0 (no negatives, no
                       Pearson residuals, no SCT residuals).
   - adata.layers["counts"]
                     → raw integer counts. int OR float-typed but
                       integer-valued. Minimum value MUST be 0.
   - adata.obsm["X_umap"] OR adata.obsm["X_tsne"]
                     → at least one 2-D embedding for visualisation.
   - adata.obs_names → unique strings.
   - adata.var_names → unique strings carrying gene symbols
                       (HGNC for human, MGI for mouse). Call
                       adata.var_names_make_unique() before export.
   - Reserved columns: don't use 'cell_id', 'cell_name', 'gene_id',
                       or 'gene_name' as obs / var column names.

   Common pitfall: counts left in adata.X, with .layers["counts"]
   missing → re-export with both populated.

2. METADATA — HARD REQUIRED (annotation, controlled vocabulary)
   -------------------------------------------------------------
   Seven keys must be set, either in the per-folder 'mus.cfg' cascade
   (legacy '.env' also accepted) or in the per-file
   '<basename>.h5ad.mus' sidecar. The sidecar overrides the cascade
   per-key.

   - organism          (e.g. human, mouse, rat, macaque, zebrafish)
   - tissue            (UBERON labels, list ok)
   - cell_type         (CL labels, list ok)
   - technology        (10x_chromium_3p_v3, Smart-seq2, NovaST, MERFISH, …)
   - suspension_type   (cell / nucleus / na)
   - access            (internal / public)
   - source            (internal / external)

   All seven are drawn from the joint vocabulary:
       https://codeberg.org/mfiers/mus/raw/branch/main/vocabulary/main.yaml
   Matching is case-insensitive, and _ ≡ - ≡ space (so
   'prefrontal_cortex', 'prefrontal-cortex', and 'Prefrontal Cortex'
   all match the same entry). 'mus vocab' prints the active lists.
   New term needed? File an issue on the mus repo to add it; do not
   invent locally.

3. METADATA — SOFT (strongly recommended; warnings on deposit)
   ------------------------------------------------------------
   - doi              (publication, if any)
   - year             (data-generation year)
   - contributor      (who prepared this deposit; freeform)
   - disease          (MONDO labels, list ok)
   - sex              (male / female / mixed / unknown)
   - condition        (freeform list — e.g. control,APOE4 carrier)
   - age              (freeform — e.g. 65, 72, 80)
   - accession        (GEO / SRA / BioProject / dbGaP id)
   - license          (e.g. CC-BY-4.0)
   - title            (freeform)
   - source_description (freeform — e.g. "from CELLxGENE 2026-04-12")

4. HOW TO SET METADATA — FOLDER vs FILE
   ---------------------------------------
   Mus has TWO independent annotation layers. Use both. They serve
   different purposes:

   4a) FOLDER-LEVEL ANNOTATION — 'mus.cfg' in a directory.
       Applies to THAT directory AND every file and subdirectory
       beneath it, recursively. Set it ONCE at the project root
       and every file under the tree inherits the values.

       Walks the cascade root-down: deeper 'mus.cfg' files override
       shallower ones, per key. So you can refine in a subfolder
       (e.g. 'access=public' in 'final/mus.cfg' while the parent
       has 'access=internal').

       Where to put what:
         - data_project           ← project root
         - eln_experiment_id      ← project root
         - organism / source      ← project root (rarely changes)
         - technology             ← project root (or per-prep subfolder)
         - tissue / suspension_type → wherever it varies

       Set via 'mus config set' from inside the folder:

           cd ~/projects/your_project

           mus eln link 12345                        # writes data_project + eln_*
           mus config set organism human
           mus config set tissue 'middle temporal gyrus,hippocampus'
           mus config set cell_type 'microglial cell'
           mus config set technology 10x_chromium_3p_v3
           mus config set suspension_type nucleus
           mus config set access internal
           mus config set source internal

       Subfolder overrides — use '-C SUBDIR' to write the
       subfolder's mus.cfg from anywhere:

           mus -C final config set access public
           mus -C final config set doi 10.1234/microglia.2025

       Values are validated against the joint vocabulary on write:
       case differences (HuMaN → human) are silently canonicalised;
       off-vocabulary values are REJECTED with a "did you mean?"
       suggestion. --force saves anyway and asks you to file an
       issue to extend the vocab.

       Inspect:
           mus config show                   # effective cascade (merged)
           mus config show --local           # just this folder's mus.cfg
           mus config files                  # paths of every mus.cfg that contributes

   4b) FILE-LEVEL ANNOTATION — '<basename>.h5ad.mus' sidecar.
       Sits next to ONE specific data file (foo.h5ad → foo.h5ad.mus).
       Applies to ONLY THAT FILE — never spreads, never inherits to
       siblings.

       Per-key precedence: file sidecar > deepest mus.cfg > shallower
       mus.cfg > … > root. So a sidecar field wins for that one file
       even when the folder cascade said something different.

       Created automatically by 'mus tag' / 'mus irods upload' /
       'mus h5ad check'. Each invocation refreshes content without
       clobbering fields it doesn't touch — diff-friendly, safe to
       commit to git.

       Free-text fields you can write with 'mus tag':
           mus tag mydata.h5ad -m "post-QC final" -t qc-pass

       Per-file overrides on biology fields don't have a flag yet —
       for one-off exceptions, create a per-subfolder mus.cfg and
       move the file there. The cascade picks it up automatically.

       Do NOT hand-edit '*.h5ad.mus' files — they carry a sha256
       paired with the file content; the mus commands keep that
       in sync.

   4c) RULE OF THUMB.
       'mus.cfg' is your RECIPE (what you intend).
       '.h5ad.mus' is the RECEIPT (what mus actually stamped on
       this specific file).
       Set the recipe once at the project root; let the receipts
       fall out per file via the standard commands.

5. VALIDATE BEFORE DEPOSIT
   -----------------------

       mus h5ad check mydata.h5ad

   This:
     - reads the cascading mus.cfg + the .h5ad.mus sidecar
     - runs every structural check from §1
     - validates every controlled-vocab value
     - stamps the sidecar with a cached PASS marker on success

   First pass is ~30s for a typical h5ad; cached subsequent passes
   are stat-fast. The cache key combines the file's sha256, the
   active vocab sha, and mus's check-version constant — any of those
   changing invalidates the cache and re-runs the full check.

6. DEPOSIT
   --------
   Mus has two deposit channels — they're orthogonal, and depositors
   typically use either or both depending on the destination.

   6a) iRODS — the lab archive (everything):

       mus irods upload -m "post-QC drop for figure 3" mydata.h5ad

       The .h5ad uploads to iRODS. All resolved metadata is stamped on
       the iRODS object as 'mus_*' AVU triplets (queryable via
       'iron query' / 'mus irods find'). The local .mus sidecar gets the
       iRODS path + persistent URL. '.h5ad.mus' and 'mus.cfg' themselves
       are NOT uploaded — they stay local.

   6b) S3 — the catalog (anthive) ingest channel:

       mus s3 login                                        # one-time setup
       mus s3 upload -m "ingest into anthive" mydata.h5ad  # per deposit

       Mus writes to a single bucket configured at login time (e.g.
       'anthive4a'), with data_project as the key prefix. So the file
       lands at s3://<bucket>/<data_project>/<basename>, e.g.
       s3://anthive4a/Fiers2025/mydata.h5ad. The same login also
       verifies the bucket exists.

       Hard-gated on the same 'mus h5ad check' PASS as iRODS upload, so
       only validated, fully-annotated files reach the catalog. The
       same 'mus_*' metadata set is stamped on the object as
       x-amz-meta-* headers, mirroring the iRODS AVU. The .h5ad.mus
       sidecar gets s3_url / s3_bucket / s3_key / s3_etag /
       s3_uploaded_at.

7. EXTRACT (anthive ingest, or any catalog reader)
   ------------------------------------------------

       # By query (no local sidecar needed):
       mus irods find --organism human --cell-type 'microglial cell' \
           --h5ad-only \
           | xargs -I{} mus irods get {}

       # By sidecar (when you already have the .h5ad.mus on disk):
       mus irods get final/mydata.h5ad.mus

   Both paths verify sha256 on download and produce a complete local
   .h5ad + .h5ad.mus pair. The sidecar is refreshed from AVU so its
   metadata is current with whatever's on the catalog.

8. COMMON FAILURE MODES
   ---------------------
   - Counts in adata.X with layers["counts"] missing → re-export
   - adata.X integer-looking → not log-normalised
   - var_names are all Ensembl IDs → remap to symbols before deposit
   - X_umap missing → embedding stored in a non-standard slot
   - Mixed nuclei + whole-cell in one file → split into two .h5ad
   - Old schema-v1 field names (celltype, preparation, internal) →
     use schema-v2: cell_type, suspension_type, access
   - Sidecar value silently ignored → check the filename is
     <basename>.h5ad.mus exactly (foo.h5ad → foo.h5ad.mus, NOT foo.mus)
   - Brand-new tissue / cell_type rejected → term not yet in the
     joint vocabulary. File an issue at the mus repo.

9. SHORTCUTS
   ----------
       mus h5ad check FILE          # validate + stamp PASS
       mus vocab                    # active joint vocabulary
       mus vocab --refresh          # re-pull vocab from upstream
       mus config show              # effective cascade values
       mus config files             # paths of every mus.cfg that contributes
       mus irods find --help        # AVU query flags
       mus irods get --help         # download options (force, verify-checksum)

For the full normative spec including the depositor self-check
Python snippet, see anthive4/doc/H5AD_INGESTION_SPEC.md.
`
