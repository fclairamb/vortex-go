# Fails when a statement block of a Go cover profile is never executed.
# With -coverpkg=./... a block appears once per test binary, so a block is
# covered when at least one of its lines has a count above zero.
NR > 1 {
	if ($NF > 0) {
		hit[$1] = 1
	} else if (!($1 in hit)) {
		hit[$1] = 0
	}
}

END {
	for (block in hit) {
		if (!hit[block]) {
			print "not covered: " block
			bad++
		}
	}
	if (bad) {
		print bad " block(s) not covered, the gate is 100%"
		exit 1
	}
	print "coverage: 100% of " length(hit) " block(s)"
}
