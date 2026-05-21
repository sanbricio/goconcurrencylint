#!/usr/bin/env bash
# Builds a per-target integration test report for the goconcurrencylint
# real-world integration workflow (.github/workflows/integration.yml).
#
# Reads run metadata from environment variables and a captured stderr file,
# then emits three outputs:
#
#   1. A plain-text sectioned report written to ${REPORT_DIR} — this is the
#      file uploaded as a workflow artifact.
#   2. A markdown section appended to $GITHUB_STEP_SUMMARY (when set) so the
#      same information is visible directly in the Actions run UI.
#   3. The resolved `report_path` and `artifact_name` exposed via
#      $GITHUB_OUTPUT for downstream workflow steps.
#
# Required environment variables:
#   TARGET_NAME           Short slug for the target (e.g. "nats-server").
#   TARGET_REPO           Full repository name (e.g. "nats-io/nats-server").
#   SCAN_PATH             Scan path passed to `go vet` (e.g. "./...").
#   STDERR_FILE           Path to the captured stderr from the lint step.
#   REPORT_DIR            Directory where the text report should be written.
#
# Optional environment variables (sensible defaults applied):
#   TARGET_SHA, TARGET_SHORT_SHA      Target repository commit.
#   LINTER_REF, LINTER_SHA            Linter ref / short commit.
#   EXIT_CODE                         Lint step exit code; empty => timeout.
#   DEPS_DURATION_S, LINT_DURATION_S  Per-phase durations in seconds.
#   MATRIX_TIMEOUT_MIN                Matrix timeout in minutes; used as the
#                                     fallback lint duration when the step
#                                     was killed before recording its time.

set -euo pipefail

# --------------------------------------------------------------------------
# Required inputs
# --------------------------------------------------------------------------
: "${TARGET_NAME:?TARGET_NAME is required}"
: "${TARGET_REPO:?TARGET_REPO is required}"
: "${SCAN_PATH:?SCAN_PATH is required}"
: "${STDERR_FILE:?STDERR_FILE is required}"
: "${REPORT_DIR:?REPORT_DIR is required}"

# --------------------------------------------------------------------------
# Optional inputs with defaults
# --------------------------------------------------------------------------
EXIT_CODE="${EXIT_CODE:-}"
DEPS_DURATION_S="${DEPS_DURATION_S:-0}"
LINT_DURATION_S="${LINT_DURATION_S:-0}"
MATRIX_TIMEOUT_MIN="${MATRIX_TIMEOUT_MIN:-0}"
TARGET_SHA="${TARGET_SHA:-unknown}"
TARGET_SHORT_SHA="${TARGET_SHORT_SHA:-unknown}"
LINTER_REF="${LINTER_REF:-unknown}"
LINTER_SHA="${LINTER_SHA:-unknown}"

mkdir -p "${REPORT_DIR}"
touch "${STDERR_FILE}"

# --------------------------------------------------------------------------
# Timeout handling
#
# When the lint step is killed by `timeout-minutes`, its outputs are never
# recorded. Fall back to sentinels that still make the report meaningful —
# in particular, attribute the configured timeout window to the lint phase
# so the resulting filename reflects the elapsed time instead of "0m00s".
# --------------------------------------------------------------------------
if [ -z "${EXIT_CODE}" ]; then
    EXIT_CODE="timeout"
    if [ "${LINT_DURATION_S}" -eq 0 ]; then
        LINT_DURATION_S=$(( MATRIX_TIMEOUT_MIN * 60 ))
    fi
fi
total_s=$(( DEPS_DURATION_S + LINT_DURATION_S ))

fmt_duration() {
    local s="$1"
    printf '%dm%02ds' $(( s / 60 )) $(( s % 60 ))
}

deps_str=$(fmt_duration "${DEPS_DURATION_S}")
lint_str=$(fmt_duration "${LINT_DURATION_S}")
total_str=$(fmt_duration "${total_s}")

slug="${TARGET_NAME}-${lint_str}"
report="${REPORT_DIR}/report-${slug}.txt"

# --------------------------------------------------------------------------
# Classify stderr output
#
# `go vet -vettool` merges two streams onto stderr: analyzer diagnostics
# (in the canonical `file.go:line:col: message` form) and build/tool
# errors. The regex below separates them so the report can present
# genuine findings and tooling failures as distinct sections.
# --------------------------------------------------------------------------
findings=$(grep -E  '^.*\.go:[0-9]+:[0-9]+:' "${STDERR_FILE}" || true)
errors=$(grep -vE '^.*\.go:[0-9]+:[0-9]+:' "${STDERR_FILE}" \
            | grep -vE '^#|^---|^go: (warning|info):|^[[:space:]]*$' || true)

finding_count=$(printf '%s' "${findings}" | grep -c . || true)
error_count=$(printf   '%s' "${errors}"   | grep -c . || true)

run_date=$(date -u '+%Y-%m-%d %H:%M:%S UTC')
go_version=$(go version | awk '{print $3, $4}')
runner_label="${RUNNER_OS:-unknown} / ${RUNNER_ARCH:-unknown}"

# --------------------------------------------------------------------------
# Plain-text sectioned report (uploaded as artifact)
# --------------------------------------------------------------------------
{
    echo "================================================================"
    echo "  goconcurrencylint — Integration Test Report"
    echo "================================================================"
    echo "  Target  : ${TARGET_REPO}"
    echo "  Date    : ${run_date}"
    echo "  Runner  : ${runner_label}"
    echo "  Go      : ${go_version}"
    echo
    echo "----------------------------------------------------------------"
    echo "  [1] METADATA"
    echo "----------------------------------------------------------------"
    echo "  Repository       : ${TARGET_REPO}"
    echo "  Target commit    : ${TARGET_SHA}"
    echo "  Scan path        : ${SCAN_PATH}"
    echo "  Linter ref       : ${LINTER_REF}"
    echo "  Linter commit    : ${LINTER_SHA}"
    echo
    echo "----------------------------------------------------------------"
    echo "  [2] EXECUTION TIMES"
    echo "----------------------------------------------------------------"
    echo "  Dependency resolution : ${deps_str}   (${DEPS_DURATION_S}s)"
    echo "  Lint execution        : ${lint_str}   (${LINT_DURATION_S}s)"
    echo "  Total measured        : ${total_str}   (${total_s}s)"
    echo
    echo "----------------------------------------------------------------"
    echo "  [3] RESULTS"
    echo "----------------------------------------------------------------"
    echo "  Exit code  : ${EXIT_CODE}"
    echo "  Findings   : ${finding_count}"
    echo "  Errors     : ${error_count}"
    echo
    echo "----------------------------------------------------------------"
    echo "  [4] FINDINGS"
    echo "----------------------------------------------------------------"
    if [ "${finding_count}" -gt 0 ]; then
        printf '%s\n' "${findings}"
    else
        echo "  (none)"
    fi
    echo
    echo "----------------------------------------------------------------"
    echo "  [5] ERRORS / STDERR"
    echo "----------------------------------------------------------------"
    if [ "${error_count}" -gt 0 ]; then
        printf '%s\n' "${errors}"
    else
        echo "  (none)"
    fi
    echo
    echo "================================================================"
    echo "  End of report"
    echo "================================================================"
} > "${report}"

# --------------------------------------------------------------------------
# Markdown summary (Actions run UI)
# --------------------------------------------------------------------------
if [ -n "${GITHUB_STEP_SUMMARY:-}" ]; then
    {
        echo "## ${TARGET_NAME} — \`${TARGET_REPO}\`"
        echo
        echo "### Metadata"
        echo "| Field | Value |"
        echo "| --- | --- |"
        echo "| Target commit | \`${TARGET_SHORT_SHA}\` |"
        echo "| Scan path | \`${SCAN_PATH}\` |"
        echo "| Linter | \`${LINTER_REF}\` @ \`${LINTER_SHA}\` |"
        echo "| Date | ${run_date} |"
        echo
        echo "### Execution times"
        echo "| Phase | Duration |"
        echo "| --- | --- |"
        echo "| Dependency resolution | ${deps_str} |"
        echo "| Lint execution | **${lint_str}** |"
        echo "| Total measured | ${total_str} |"
        echo
        echo "### Results"
        echo "| Metric | Value |"
        echo "| --- | --- |"
        echo "| Exit code | \`${EXIT_CODE}\` |"
        echo "| Findings | **${finding_count}** |"
        echo "| Errors | **${error_count}** |"
        echo
        if [ "${finding_count}" -gt 0 ]; then
            echo "<details><summary>Findings (first 50)</summary>"
            echo
            echo '```'
            printf '%s\n' "${findings}" | head -50
            echo '```'
            echo
            echo "</details>"
        fi
        if [ "${error_count}" -gt 0 ]; then
            echo
            echo "<details><summary>Linter errors / stderr (first 50)</summary>"
            echo
            echo '```'
            printf '%s\n' "${errors}" | head -50
            echo '```'
            echo
            echo "</details>"
        fi
        echo
    } >> "${GITHUB_STEP_SUMMARY}"
fi

# --------------------------------------------------------------------------
# Workflow outputs
# --------------------------------------------------------------------------
if [ -n "${GITHUB_OUTPUT:-}" ]; then
    echo "report_path=${report}"        >> "${GITHUB_OUTPUT}"
    echo "artifact_name=report-${slug}" >> "${GITHUB_OUTPUT}"
fi

echo "Report written to: ${report}"
