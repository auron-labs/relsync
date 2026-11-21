# Golden corpus

Recorded release file lists, with the file expected for each architecture and
installer preference. `corpus_test.go` resolves every block and fails on a
difference, so a change to the scoring tables is reviewed against these picks.

Format, one block per repository:

    == owner/repo kind=app|tool [topics=a,b]
    <file name> [size in bytes]
    -> <target>: <expected file, or none>

- `kind` is the kind the app is expected to guess from the files and topics.
- A target is `all`, `x64`, `arm64`, or one of those followed by `/installers` or
  `/portable`; the preference applies to the four combinations of x64 or arm64
  with installers or portable. Later lines override earlier ones.
- Lines starting with `#` are comments, explaining a pick that surprises.

Lists are abridged recordings: the files that matter for choosing, plus the
noise (checksums, other systems' packages) a real release carries.
