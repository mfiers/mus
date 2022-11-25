

watch_build_mac:
	ls * \
	| entr nim c  \
		-d:release \
		-o:mus.mac \
		--verbosity:0 \
		mus.nim