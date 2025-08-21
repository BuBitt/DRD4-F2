drd4
====

Small CLI to parse a FASTA, run MAFFT alignment, prefer GenBank translations (NCBI efetch) with a file-backed cache, and fallback to external translators (transeq/seqkit).

Quick start
-----------

Build:

```sh
go build -o drd4 ./cmd
```

Run:

```sh
./drd4 -in drd4-tdah.fasta -out drd4-database.json
```

Useful flags:

- `-config` path to `config.json`
- `-in` input FASTA
- `-out` output JSON
- `-external` enable external translator fallback (transeq/seqkit)
- `-mafft-args` additional args for MAFFT (quoted)
- `-dry-run` don't call external tools or write files
- `-verbose` enable debug logs

Build-time version
------------------
Set the build-time version with:

```sh
go build -ldflags "-X main.version=1.2.3" -o drd4 ./cmd
```
