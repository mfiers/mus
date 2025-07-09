SHELL=bash

build:
	(cd .. ; hatch build)
	echo "#### Get most recent Wheel"
	rm *whl ; cp $$(ls -t ../dist/*.whl | head -1) .
	echo "#### Start apptainer build"
	apptainer build -F mus.sif Build
	echo "#### Cleanup"
	rm *whl

sync:
	scp mus.sif hpc:/staging/leuven/stg_00079/singularity/