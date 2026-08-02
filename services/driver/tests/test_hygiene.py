"""AST-based hygiene guard: no bare print() in this service's own production code
(mirrors libs/py-pitwall/tests/test_hygiene.py and libs/go-pitwall/hygiene — NFR20)."""

import ast
from pathlib import Path


def _production_files():
    pkg_dir = Path(__file__).resolve().parent.parent / "driver"
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
                # driver.main deliberately uses print() exactly once: reporting a
                # ConfigError before the structured logger exists to log to (see its
                # own comment). Every other production file must have none.
                if path.name == "main.py":
                    continue
                violations.append(f"{path}:{node.lineno}")
    assert not violations, f"bare print() found in production code: {violations}"


def test_main_has_exactly_one_documented_print_exception():
    main_py = Path(__file__).resolve().parent.parent / "driver" / "main.py"
    tree = ast.parse(main_py.read_text(encoding="utf-8"), filename=str(main_py))
    prints = [
        node
        for node in ast.walk(tree)
        if isinstance(node, ast.Call) and isinstance(node.func, ast.Name) and node.func.id == "print"
    ]
    assert len(prints) == 1, (
        f"expected exactly one documented print() (the pre-logger config-error path), found {len(prints)}"
    )
