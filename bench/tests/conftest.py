import subprocess
from pathlib import Path

import pytest

REPO_ROOT = Path(__file__).resolve().parents[2]


def pytest_configure(config):
    config.addinivalue_line(
        "markers",
        "integration: builds or runs the real docstomd binary; excluded from `make bench-test`, run by `make bench-test-integration`",
    )


@pytest.fixture(scope="session")
def docstomd_binary(tmp_path_factory):
    binary = tmp_path_factory.mktemp("bin") / "docstomd"
    subprocess.run(["go", "build", "-o", str(binary), "./cmd/docstomd"], cwd=REPO_ROOT, check=True)
    return binary
