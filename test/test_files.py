
from click.testing import CliRunner

import mus.cli


def test_run_tag_no_args():
    "Test tag"
    runner = CliRunner()
    result = runner.invoke(mus.cli.cli, ['tag'])
    assert result.exit_code == 2
    assert 'Error: Missing argument' in result.output


def test_file():
    """ Check if mus file works, does it return the correct checksum?
    """
    runner = CliRunner()
    result = runner.invoke(
        mus.cli.cli,
        'file test/data/test.txt'.split())
    assert result.exit_code == 0
    assert 'c5ac357f04cc9aaa825667754a271e87346ea1c6c18025fd593d686c6d321496' in result.output


def test_run_tag_help():
    "Run and check whether tagging a file works"

    runner = CliRunner()
    result = runner.invoke(
        mus.cli.cli,
        'tag test/data/test.txt MuStEsT running mus tests'.split())
    assert result.exit_code == 0
    assert not result.output.strip()  # empty output

    result = runner.invoke(
        mus.cli.cli,
        'file test/data/test.txt'.split())
    assert result.exit_code == 0
    assert 'c5ac357f04cc9aaa825667754a271e87346ea1c6c18025fd593d686c6d321496' \
        in result.output
    assert 'MuStEsT running mus tests' in result.output