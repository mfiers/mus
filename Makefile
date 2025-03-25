SHELL=bash

build:
	rm dist/*
	hatch build
	cp dist/mus-*.whl dist/mus-latest.whl

# .SILENT:
# compile_mac:
# 	echo -n '..'
# 	nim c  \
# 		--hints:off \
# 		-d:release \
# 		-o:mus.mac \
# 		--verbosity:1 \
# 		mus.nim
# 	echo -n '|'

# watch_build_mac:
# 	ls * | entr ${MAKE} compile_mac

# .SILENT:
# compile_linux:
# 	echo -n '..'
# 	nim c  \
# 		--hints:off \
# 		-d:release \
# 		-o:mus.linux \
# 		--verbosity:1 \
# 		mus.nim
# 	echo -n '|'
