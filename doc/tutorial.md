# mus tutorial

The tutorial is split into five focused docs. Read in order if you're new; jump to the one you need if you're not.

| # | Doc | Goal |
|---|---|---|
| 01 | [Install & first-run setup](01_install_and_setup.md) | install mus, authenticate iRODS + eLab, install shell completion. One-time work per machine, ≈ 3 minutes. |
| 02 | [Annotation & vocabularies](02_annotation_and_vocabularies.md) | how to describe data with cascading `mus.cfg` + per-file sidecars + the joint controlled vocabulary. The data-description layer all upload paths share. |
| 03 | [ELN linking & iRODS upload](03_eln_and_irods.md) | link a folder to an eLab experiment, then back the data up to the lab's iRODS archive. Verify + restore + safe-delete. |
| 04 | [h5ad deposit (for anthive)](04_h5ad_deposit.md) | prep single-cell `.h5ad` files for the lab's anthive catalog. Structural check + S3 upload. |
| 05 | [Differential-expression deposit](05_de_deposit.md) | prep DE result tables (DESeq2 / edgeR / scanpy / Seurat / glmmSeq / glmmTMB / long) for anthive. Co-ingest with an h5ad supported. |

---

## Which doc do I need?

```
                 just installing mus?  →  01
            need to annotate data?     →  02  (read 01 first if you haven't)
       backing up data to iRODS?       →  02 + 03
   pushing an h5ad to the catalog?     →  02 + 04   (+ 03 for the iRODS archive copy)
  pushing DE results to the catalog?   →  02 + 05
        co-ingesting h5ad + DE?        →  02 + 04 + 05
```

---

## Reference docs (not tutorial)

- [quickstart.md](quickstart.md) — full reference manual: every flag, env var, edge case.
- `mus h5ad howto` — h5ad structural rules + vocab field list + common failure modes (printed to stdout).
- `mus de howto` — DE format signatures + field reference (printed to stdout).
- [joint vocabulary](https://codeberg.org/mfiers/mus/src/branch/main/vocabulary/main.yaml) — controlled-value lists.

### Ontology browsers

- UBERON (anatomy): <https://www.ebi.ac.uk/ols4/ontologies/uberon>
- CL (cell types): <https://www.ebi.ac.uk/ols4/ontologies/cl>
- MONDO (diseases): <https://www.ebi.ac.uk/ols4/ontologies/mondo>
- EFO (assays / protocols): <https://www.ebi.ac.uk/ols4/ontologies/efo>
