// Bound cross-test contention so blocking I/O tests leave room for continuations.
[assembly: Parallelize(Workers = 4, Scope = ExecutionScope.MethodLevel)]
