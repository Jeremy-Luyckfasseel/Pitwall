"""AST-based hygiene guard: no bare print() in this library's own production code
(mirrors libs/go-pitwall/hygiene/hygiene_test.go's TestNoBarePrintStatements — every
service is held to the structured-logging rule, NFR20)."""

import ast
from pathlib import Path


def _production_files():
    pkg_dir = Path(__file__).resolve().parent.parent / "pitwall"
    return sorted(pkg_dir.rglob("*.py"))


def test_no_bare_print_statements():
    violations = []
    for path in _production_files():
        tree = ast.parse(path.read_text(encoding="utf-8"), filename=str(path))
        for node in ast.walk(tree):
            if (
                isinstance(node, ast.Call)
                and isinstance(node.func, ast.Name)
                and node.func.id == "print"
            ):
                violations.append(f"{path}:{node.lineno}")
    assert not violations, f"bare print() found in production code: {violations}"


def test_production_files_exist():
    # Guards against the walk silently finding nothing (a passing-by-vacuous-truth test).
    assert len(_production_files()) > 0
