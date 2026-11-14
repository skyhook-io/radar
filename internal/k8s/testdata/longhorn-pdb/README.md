# Longhorn PDB identity fixtures

Synthetic Kubernetes snapshots derived from official longhorn-manager source.
These are not live cluster captures or version runtime tests.

| Fixture | Manager source | Process schema |
| --- | --- | --- |
| v1.10.2 | 020cf3d4c6935a38b57fe6e1305e1e7fb048bd20 | engines, replicas, deprecated instances |
| v1.11.3 | 4008b91b9ce8251d6905038a796ab9762e3bb273 | engines, replicas |
| v1.12.2 | f6144ba7547c81fd1297618de521ecfef9584ae1 | engines, replicas, engine frontends |
| v1.13.0 | 20d9c0790312b45aafd5668674e73dbf2eba1832 | engines, replicas, engine frontends |

Each source commit is in [longhorn-manager](https://github.com/longhorn/longhorn-manager).
Identity follows `generateInstanceManagerPDBManifest` in
`controller/instance_manager_controller.go`, `GetInstanceManagerLabels`,
`GetImageCanonicalName` and `GetInstanceManagerImageChecksumName` in
`types/types.go`, and `GetStringChecksum` in `util/util.go`.
The image label is the first eight lowercase hex SHA512 characters after
replacing colons and slashes with hyphens and trimming surrounding whitespace.
The Pod controller reference binds to the live IM UID; the PDB has no generated
owner reference or top-level labels.

The PDB identity shape is also unchanged in the audited 1.8.2, 1.9.x, 1.10.0/1,
1.11.0/1/2 and 1.12.0/1 sources. Tests intentionally do not copy Longhorn's
version-sensitive removal predicates. Process entries, drain policies and
autoscaler integration cannot turn a zero-disruption snapshot into safe drain
or healthy storage evidence in Radar.
