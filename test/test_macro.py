
import shutil
import tempfile
from pathlib import Path
from uuid import uuid4

from click.testing import CliRunner, Result

import mus.cli

TEST_FOLDER = Path('./test/data')

TEST_MACRO_NAME = 'testtesttesttesttest'


def run_macro(macro: str) -> Result:
    """Helper function to run a macro

    Args:
        macro (str): Macro string to execut

    Returns:
        Result: CliRunner result of the run.
    """
    runner = CliRunner()
    to_execute = ' 42  m ' + macro
    print(to_execute)
    result = runner.invoke(mus.cli.cli,
                           ['macro', 'stdin-exe'],
                           input=to_execute)
    return result


def test_run_macro_ls():
    "Test an ls"
    result = run_macro('ls test/data')
    assert 'test01.txt' in result.output
    assert 'test02.txt' in result.output
    assert result.exit_code == 0


def test_run_macro_more_complex():
    "Test a slightly more complex macro with a bash loop"
    result = run_macro('for x in $(seq 5); do echo $x; done')
    output = " ".join(result.output.split())
    assert output == '1 2 3 4 5'
    assert result.exit_code == 0


def test_run_macro_elements():
    "Test macro with elements"

    test_folder = Path(tempfile.mkdtemp())
    macro = 'cat {test/data/*.txt} | echo "{%n}" > ' + \
            str(test_folder) + \
            '/{%s}.out'

    result = run_macro(macro)
    assert result.exit_code == 0

    outfiles = list(test_folder.glob('*'))
    outnames = [x.name for x in outfiles]
    contents = [open(x).read().strip() for x in outfiles]

    assert 'test01.out' in outnames
    assert 'test02.out' in outnames
    assert 'test01.txt' in contents
    assert 'test02.txt' in contents
    assert len(outfiles) == len(contents) == 2


def test_save_macro():
    "Can mus save a macro and return it's contents?"

    from mus.macro import delete_macro, load_macro

    macro_to_save = f'echo "{str(uuid4())}"'
    result = run_macro(f'-s{TEST_MACRO_NAME} {macro_to_save}')
    assert result.exit_code == 0
    macro_saved = load_macro(TEST_MACRO_NAME)
    assert macro_saved == macro_to_save
    delete_macro(TEST_MACRO_NAME)


def test_run_saved_macro():
    "Can mus run a pre-saved macro?"
    from mus.macro import delete_macro

    unique_string = str(uuid4())
    macro = f'echo "{unique_string}"'
    result = run_macro(f'-s{TEST_MACRO_NAME} {macro}')
    print(result)
    assert result.exit_code == 0
    assert result.output.strip() == unique_string
    delete_macro(TEST_MACRO_NAME)
