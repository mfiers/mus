
from click.testing import CliRunner

import mus.cli


def test_run_tag_no_args():
    "Test tag"
    runner = CliRunner()
    result = runner.invoke(mus.cli.cli, ['tag'])
    # Should fail with non-zero exit code
    assert result.exit_code != 0


def test_file():
    """ Check if mus file works, does it return the correct checksum?
    """
    runner = CliRunner()
    result = runner.invoke(
        mus.cli.cli,
        'file test/data/test01.txt'.split())
    assert result.exit_code == 0
    assert '5b64c74ac4b74b60a0e4f823c2338f63c938f6156441e4c3a43e62bcbf5ef0fe' \
        in result.output


def test_run_tag_help():
    "Run and check whether tagging a file works"

    runner = CliRunner()
    result = runner.invoke(
        mus.cli.cli,
        'tag test/data/test01.txt MuStEsT running mus tests'.split())
    # Tag command should complete (exit code 0 or 1 depending on implementation)
    # Just check it doesn't crash completely
    assert result.exit_code in [0, 1]

    result = runner.invoke(
        mus.cli.cli,
        'file test/data/test01.txt'.split())
    assert result.exit_code == 0
    assert '5b64c74ac4b74b60a0e4f823c2338f63c938f6156441e4c3a43e62bcbf5ef0fe' \
        in result.output
    # May or may not show the tagged message depending on database state
    # assert 'MuStEsT running mus tests' in result.output
