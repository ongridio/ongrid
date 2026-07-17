[
  .manifests[]?.platform
  | "\(.os)/\(.architecture)"
]
| index("linux/amd64") != null
  and index("linux/arm64") != null
