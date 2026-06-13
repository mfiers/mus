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
   all match the same entry). 'mus vocab show' prints the active lists.
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

5. DOCUMENTING THE DATA (.md FILES — depositor-written)
   ----------------------------------------------------
   Two human-written markdown files complement each deposit. They
   exist for HUMAN READERS — colleagues browsing the iRODS / S3
   collection before any catalog gets involved. Anthive ingest can
   also use them as a starting point, but they're just regular
   markdown — no required schema, no YAML frontmatter mus cares
   about, no fields mus validates.

   Mus does NOT generate them. Mus DOES upload them alongside the
   data when present (see §7).

   5a) FOLDER-LEVEL: 'index.md'
       One per project folder. Describes the project as a whole:
       what's in the folder, who put it together, what each dataset
       is for, why it exists. Free prose. Examples of what to write:

           # Microglia pilot (Fiers2025)

           snRNA-seq of human prefrontal cortex microglia from the
           Sun lab, sorted PU.1+ nuclei, 10x Multiome v2.
           Datasets in this folder:

           - mydata.h5ad       full 10x snRNA-seq output
           - mydata_qc.h5ad    after doublet removal + filtering
           - mydata_atlas.h5ad mapped to the CellxGene reference

           Acquired 2025-Q3. Manuscript in prep (Fiers, et al.).

   5b) FILE-LEVEL: '<basename>.md'
       Sits next to each .h5ad as 'mydata.h5ad.md'. Describes that
       ONE file: what's special about it, what processing produced
       it, known caveats. Free prose:

           # mydata.h5ad

           Microglia-only subset of the full PFC cohort. PU.1+
           sorted nuclei, n=73888 cells across 7 donors.
           Normalised with scanpy.pp.normalize_total → log1p.
           UMAP from PCA(30) + Harmony on donor.

           Caveats:
             - donor 4 has high mito% — consider filtering.
             - clusters 8 and 9 are likely doublets — see notebook
               on the pika project page.

   5c) RELATIONSHIP TO MUS'S OWN FILES

       - mus.cfg + *.h5ad.mus — structured, vocab-validated,
         auto-stamped. Mus owns them.
       - index.md + *.md — free prose. Depositor owns them. Mus
         does NOT regenerate, validate, or schema-check them —
         they're just markdown.

       Both layers travel together on upload, but how depends on
       the destination — see §7 for the exact deposit shape.
       Short version: every upload puts a per-folder
       'mus_metadata_<timestamp>.tar.gz' next to the data. S3 puts
       the prose .md inside that archive; iRODS uploads .md as
       standalone objects instead and keeps the archive to
       mus.cfg + .mus only.

6. VALIDATE BEFORE DEPOSIT
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

7. DEPOSIT
   --------
   Mus has two deposit channels — they're orthogonal, and depositors
   typically use either or both depending on the destination.

   Both deposits refuse multi-source-folder invocations and require
   data_project be set on the cascade. Both build a per-folder
   archive named:

       mus_metadata_<UTC-timestamp>.tar.gz

   …but the archive contents differ between iRODS and S3 (see below).
   Built AFTER the primaries upload so the embedded sidecars carry
   the freshly-stamped irods_* / s3_* fields.

   7a) iRODS — the lab archive:

       mus irods upload -m "post-QC drop for figure 3" mydata.h5ad
       mus irods upload -m "with notes" mydata.h5ad notes.md

       Accepts any file extension. *.mus sidecars passed as args
       are SKIPPED (mus owns them, they ride in the archive). Each
       file lands as a standalone iRODS object in the target
       collection — .md files travel openly as data objects so a
       colleague can 'iget' them without unpacking anything.

       The per-folder archive holds ONLY the structured metadata:
         mus.cfg                      (folder's own config, if any)
         <basename>.mus               (one per uploaded primary)

       No .md files in the iRODS archive — those are full deposits
       in their own right when explicitly named.

       The 'mus_*' strict-vocab metadata is stamped on EVERY
       uploaded iRODS object (h5ad, md, anything else) as AVU
       triplets, queryable via 'iron query' / 'mus irods find'.

   7b) S3 — the catalog (anthive) ingest channel:

       mus s3 login                                        # one-time setup
       mus s3 upload -m "ingest into anthive" mydata.h5ad  # per deposit
       mus s3 upload -m "site banner" --static banner.png  # static-only OK

       Positional args MUST be .h5ad. Mus writes to a single bucket
       configured at login time (e.g. 'anthive4a'), with data_project
       as the key prefix. S3 layout is at depth ≤ 1 below
       <data_project>/:

         s3://anthive4a/Fiers2025/mydata.h5ad                (default)
         s3://anthive4a/Fiers2025/mus_metadata_<stamp>.tar.gz
         s3://anthive4a/Fiers2025/rev2/mydata.h5ad           (--subfolder rev2)
         s3://anthive4a/Fiers2025/rev2/mus_metadata_<stamp>.tar.gz
         s3://anthive4a/Fiers2025/static/<basename>          (--static)

       --subfolder NAME (optional, single segment) writes the .h5ad
       and its metadata archive under <data_project>/NAME/ instead
       of the data_project root. Useful for revisions, drafts,
       embargoed releases. 'static', '.', '..' and any name
       containing a path separator are refused.

       --static FILE (repeatable) ALWAYS lands at
       <data_project>/static/<basename> regardless of --subfolder.
       Use for auxiliary assets (figures, JSON, PDFs) that the
       anthive web view needs but that aren't biology data
       themselves. Static uploads carry no biology AVU and skip the
       h5ad gate; only the upload message lands as x-amz-meta.
       Static-only invocations (no .h5ad positionals) are accepted.

       The S3 metadata archive bundles EVERYTHING the catalog
       ingester needs for the h5ad deposit (folder mus.cfg, folder
       index.md, per-primary .h5ad.mus + sibling .h5ad.md). Hard-
       gated on the same 'mus h5ad check' PASS as iRODS upload.
       'mus_*' biology metadata lands on each h5ad S3 object as
       x-amz-meta-* headers, mirroring the AVU set.

8. EXTRACT (anthive ingest, or any catalog reader)
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

9. COMMON FAILURE MODES
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

10. SHORTCUTS
    ---------
       mus h5ad check FILE          # validate + stamp PASS
       mus vocab show               # active joint vocabulary (every field)
       mus vocab show tissue        # one field's allowed values
       mus vocab refresh            # force re-pull from upstream
       mus config show              # effective cascade values
       mus config files             # paths of every mus.cfg that contributes
       mus irods find --help        # AVU query flags
       mus irods get --help         # download options (force, verify-checksum)

For the full normative spec including the depositor self-check
Python snippet, see anthive4/doc/H5AD_INGESTION_SPEC.md.
`
