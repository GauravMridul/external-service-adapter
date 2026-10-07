package sequence_service

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"math/rand"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/itlightning/dateparse"

	"esa/internal/app/dto/common_dto"
	commoninit "esa/internal/app/init"
	"esa/internal/app/models"
	"esa/internal/app/utility"

	"github.com/dmi-infotech/common-modules/go/contracts"
)

// ExpressionProcessor handles all placeholder resolution and expression evaluation
type ExpressionProcessor struct {
	// Compiled regex patterns for better performance
	angleBracketRegex      *regexp.Regexp
	doubleParenthesesRegex *regexp.Regexp
	expressionRegex        *regexp.Regexp
	fallbackChainRegex     *regexp.Regexp

	// logger is the process logger when common-modules is initialized, otherwise nil (e.g. in unit
	// tests that construct the processor directly). All logging must nil-check via ep.logError.
	logger contracts.Logger
}

// NewExpressionProcessor creates a new expression processor with compiled regex patterns
func NewExpressionProcessor() *ExpressionProcessor {
	return &ExpressionProcessor{
		angleBracketRegex:      regexp.MustCompile(`<([A-Za-z0-9_]+(?:\[[^\]]*\])?(?:\.[A-Za-z0-9_]+)*)>`), // Supports object.field and object.relation__r.field (multi-dot)
		doubleParenthesesRegex: regexp.MustCompile(`\(\((.+?)\)\)`),
		expressionRegex:        regexp.MustCompile(`\{\{(.+)\}\}`), // Use greedy matching to find all expressions
		fallbackChainRegex:     regexp.MustCompile(`\s*\|\|\s*`),
		logger:                 commoninit.GetLoggerSafe(),
	}
}

// logError emits an error-level log when a logger is available. It is safe to call when the
// processor was constructed without an initialized logger (the message is silently dropped).
func (ep *ExpressionProcessor) logError(msg string, keysAndValues ...interface{}) {
	if ep == nil || ep.logger == nil {
		return
	}
	ep.logger.Errorw(msg, keysAndValues...)
}

// logWarn reports a recoverable configuration problem. Like logError it is nil-safe, so unit tests
// that construct the processor directly (no common-modules init) can call it freely.
func (ep *ExpressionProcessor) logWarn(msg string, keysAndValues ...interface{}) {
	if ep == nil || ep.logger == nil {
		return
	}
	ep.logger.Warnw(msg, keysAndValues...)
}

// TypeMode defines output type behavior
type TypeMode string

const (
	TypeModeString TypeMode = "string" // Always return strings (for headers/URLs)
	TypeModeTyped  TypeMode = "typed"  // Return proper JSON types (for request bodies)
)

// PlaceholderCleanupOptions configures selective skipping of post-processing steps after placeholder
// resolution (see cleanupFallbackChainArtifacts). Nil means apply all default cleanup steps.
type PlaceholderCleanupOptions struct {
	Omit []string
}

// Documented omit step IDs for additional_config.placeholder_cleanup (per field).
const (
	// CleanupOmitRemoveDoublePipe skips removing literal "||" from the final string. Required for
	// vendor pipe-delimited payloads where empty fields appear as adjacent delimiters ("||").
	CleanupOmitRemoveDoublePipe = "remove_double_pipe"
	// CleanupOmitTrimPipeSpaceEdges skips trimming a leading "| " or trailing " |" from the final string.
	CleanupOmitTrimPipeSpaceEdges = "trim_pipe_space_edges"
)

func cleanupOptsShouldOmit(opts *PlaceholderCleanupOptions, step string) bool {
	if opts == nil {
		return false
	}
	for _, o := range opts.Omit {
		if o == step {
			return true
		}
	}
	return false
}

// ctxErr returns ctx.Err() but is nil-safe: a nil context is treated as having no deadline (no
// cancellation). This lets the resolution loops accept a nil/background context unchanged while still
// honoring a real request deadline when one is threaded in.
func ctxErr(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	return ctx.Err()
}

// ProcessPlaceholders processes all placeholder types in the given text.
// cleanupOpts may be nil for default cleanup behavior.
func (ep *ExpressionProcessor) ProcessPlaceholders(text string, masterDTO *common_dto.MasterDTO, serviceNameMap map[string]*models.EsaLog, placeholderCache map[string]string, cleanupOpts *PlaceholderCleanupOptions) string {
	return ep.ProcessPlaceholdersCtx(context.Background(), text, masterDTO, serviceNameMap, placeholderCache, cleanupOpts)
}

// ProcessPlaceholdersCtx is the context-aware variant of ProcessPlaceholders. The deadline carried by
// ctx (the overall process-sequence request budget set in the controller) is observed inside the
// resolution loops, so a pathological payload aborts at the request deadline instead of spinning
// CPU-bound. A nil or context.Background() ctx imposes no deadline (identical to legacy behavior).
func (ep *ExpressionProcessor) ProcessPlaceholdersCtx(ctx context.Context, text string, masterDTO *common_dto.MasterDTO, serviceNameMap map[string]*models.EsaLog, placeholderCache map[string]string, cleanupOpts *PlaceholderCleanupOptions) string {
	result := ep.ProcessPlaceholdersWithTypeCtx(ctx, text, TypeModeString, masterDTO, serviceNameMap, placeholderCache, cleanupOpts)
	return result.(string)
}

// ProcessPlaceholdersWithType processes placeholders with type mode support.
// cleanupOpts may be nil for default cleanup behavior.
// Optional itemContext (variadic) is used for fan-out: when provided, ((item.fieldPath)) is resolved from it.
// This keeps the original (no-ctx) signature for existing callers/tests and imposes no deadline; it
// delegates to ProcessPlaceholdersWithTypeCtx with context.Background().
func (ep *ExpressionProcessor) ProcessPlaceholdersWithType(text string, typeMode TypeMode, masterDTO *common_dto.MasterDTO, serviceNameMap map[string]*models.EsaLog, placeholderCache map[string]string, cleanupOpts *PlaceholderCleanupOptions, itemContext ...map[string]interface{}) interface{} {
	return ep.ProcessPlaceholdersWithTypeCtx(context.Background(), text, typeMode, masterDTO, serviceNameMap, placeholderCache, cleanupOpts, itemContext...)
}

// ProcessPlaceholdersWithTypeCtx is the context-aware implementation of placeholder resolution.
// It checks ctx only at the resolution-loop boundaries (the outer loop of processExpressionPlaceholders
// via the ctx passed there, and between the re-evaluation passes below) — the only places a large or
// pathological payload can spin. On deadline/cancellation it stops and returns best-effort output;
// callers detect cancellation via ctx.Err() and abort before making any network call. The deadline is the
// single request-scoped budget threaded down from the controller (never a fresh per-step timer).
func (ep *ExpressionProcessor) ProcessPlaceholdersWithTypeCtx(ctx context.Context, text string, typeMode TypeMode, masterDTO *common_dto.MasterDTO, serviceNameMap map[string]*models.EsaLog, placeholderCache map[string]string, cleanupOpts *PlaceholderCleanupOptions, itemContext ...map[string]interface{}) interface{} {
	if text == "" {
		return text
	}
	// If the entire input is a single outer {{...}} expression (possibly with nested expressions inside)
	// and has only surrounding whitespace outside that expression, we trim the final string output.
	// This prevents configs like "{{NUMERIC:...}} " from returning "0 " due to accidental trailing spaces.
	trimOuterWhitespace := false
	trimmedInput := strings.TrimSpace(text)
	if trimmedInput != text && strings.HasPrefix(trimmedInput, "{{") && strings.HasSuffix(trimmedInput, "}}") {
		trimOuterWhitespace = true
	}
	var itemCtx map[string]interface{}
	if len(itemContext) > 0 {
		itemCtx = itemContext[0]
	}

	isDebugTarget := debugPipeline && strings.Contains(text, "CONDITION:")
	if isDebugTarget {
		fmt.Printf("\n[PIPELINE] ========== START ==========\n[PIPELINE] INPUT: %s\n", debugTruncate(text, 500))
	}

	// 1. First normalize all database placeholders to lowercase
	text = ep.normalizeDatabasePlaceholders(text)

	// 2. Process expression placeholders first {{...}} (including NUMERIC with fallbacks)
	result := ep.processExpressionPlaceholders(ctx, text, masterDTO, serviceNameMap)
	if isDebugTarget {
		fmt.Printf("[PIPELINE STEP 2] after processExpressionPlaceholders:\n  %s\n", debugTruncate(result, 500))
	}

	// 3. Defer fallback-chain collapsing until after DB + service placeholders are resolved (see step 5c).

	// 4. Process database data placeholders <...>
	result = ep.processDbDataPlaceholders(result, masterDTO)
	if isDebugTarget {
		fmt.Printf("[PIPELINE STEP 4] after processDbDataPlaceholders:\n  %s\n", debugTruncate(result, 500))
	}

	// 5. Process service response placeholders ((...)) including ((item.xxx)) when itemContext provided
	result = ep.processServiceResponsePlaceholders(result, serviceNameMap, itemCtx)
	if isDebugTarget {
		fmt.Printf("[PIPELINE STEP 5] after processServiceResponsePlaceholders:\n  %s\n", debugTruncate(result, 500))
	}

	// 5b. Re-run expression placeholders so any {{ }} left (e.g. outer CONDITION that contained
	//     ((Service)) and is now JSON in condition after step 5) are evaluated. Loop until no {{
	//     remain (max 5 passes) so nested outer CONDITIONs are fully resolved.
	for pass := 0; pass < 5; pass++ {
		// Observe the request deadline between passes so a pathological payload cannot keep
		// re-scanning multi-MB text past the overall request budget.
		if ctxErr(ctx) != nil {
			break
		}
		if isDebugTarget {
			fmt.Printf("[PIPELINE STEP 5b] pass=%d input:\n  %s\n", pass, debugTruncate(result, 500))
		}
		next := ep.processExpressionPlaceholders(ctx, result, masterDTO, serviceNameMap)
		if isDebugTarget {
			fmt.Printf("[PIPELINE STEP 5b] pass=%d output:\n  %s\n", pass, debugTruncate(next, 500))
		}
		if next == result && !strings.Contains(result, "{{") {
			break
		}
		result = next
		if !strings.Contains(result, "{{") {
			break
		}
	}

	// 5c. Re-run fallback chains after DB + service placeholders are resolved.
	// This enables config patterns like "{{ARRAY:...}} || []" and "Bearer <t1> || <t2>" to collapse to a single value
	// without leaving trailing "[]"/defaults in the output.
	// When remove_double_pipe cleanup is omitted, skip this step: text-level fallback also splits on "||" and would
	// corrupt vendor pipe-delimited payloads (empty fields as "||"). Same field must not mix || fallbacks then.
	if !cleanupOptsShouldOmit(cleanupOpts, CleanupOmitRemoveDoublePipe) {
		result = ep.processFallbackChainsAtTextLevel(result, masterDTO)
	}

	// 6. Clean up any remaining fallback chain artifacts
	result = ep.cleanupFallbackChainArtifacts(result, cleanupOpts)
	if isDebugTarget {
		fmt.Printf("[PIPELINE] FINAL RESULT: %s\n[PIPELINE] ========== END ==========\n\n", debugTruncate(result, 500))
	}

	// Apply type conversion based on mode
	if typeMode == TypeModeTyped {
		return ep.convertToProperType(result)
	}

	// In string mode, clean up type markers
	out := ep.cleanupTypeMarkers(result)
	if trimOuterWhitespace {
		out = strings.TrimSpace(out)
	}
	return out
}

// normalizeDatabasePlaceholders converts all <...> placeholders to lowercase for consistent processing
func (ep *ExpressionProcessor) normalizeDatabasePlaceholders(text string) string {
	// Then normalize individual placeholders
	return ep.angleBracketRegex.ReplaceAllStringFunc(text, func(match string) string {
		content := match[1 : len(match)-1] // Remove < and >

		// Handle conditional syntax: Object[condition].field
		objectName, condition, fieldName, isConditional := utility.ParseConditionalPlaceholder(content)
		if isConditional {
			// Normalize components and reconstruct
			normalizedObjectName := strings.ToLower(strings.TrimSpace(objectName))
			normalizedCondition := strings.TrimSpace(condition)
			normalizedFieldName := strings.TrimSpace(fieldName)
			return "<" + normalizedObjectName + "[" + normalizedCondition + "]." + normalizedFieldName + ">"
		}

		// Handle simple case using shared utility
		if objectName != "" && fieldName != "" {
			// Normalize object name to lowercase, keep field name as-is
			normalizedObjectName := strings.ToLower(strings.TrimSpace(objectName))
			normalizedFieldName := strings.TrimSpace(fieldName)
			return "<" + normalizedObjectName + "." + normalizedFieldName + ">"
		}

		return match // Return unchanged if it doesn't match expected patterns
	})
}

// debugExpressionBraces enables temporary logging for brace matching (set true when debugging).
const debugExpressionBraces = false

// debugPipeline enables temporary logging across the full processing pipeline.
// Set to true, run tests, then set back to false. Remove before merging.
const debugPipeline = false

// debugAnalyticsAA was used for temporary logging; set true to re-enable if needed.
const debugAnalyticsAA = false

func debugTruncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

// findMatchingClosingDoubleBraces returns the end index (exclusive) of the }}
// that matches the {{ at start. Uses depth counting: start with depth 1 after the
// opening {{, decrement on }}, increment on {{; return when depth reaches 0.
// Does not count {{ or }} when inside a quoted string (single or double) so that
// CONDITION literals like 'Self employed' and JSON-in-condition (e.g. after
// ((Service)) is replaced in a later pass) do not break brace matching.
// Returns -1 if no matching }} is found.
func (ep *ExpressionProcessor) findMatchingClosingDoubleBraces(text string, start int) int {
	if start < 0 || start+2 > len(text) || text[start] != '{' || text[start+1] != '{' {
		return -1
	}
	depth := 1
	inSingleQuotes := false
	inDoubleQuotes := false
	for i := start + 2; i < len(text); i++ {
		c := text[i]
		// Skip escaped chars inside EITHER quote type. The single-quote wrap in
		// processExpressionPlaceholders escapes ' -> \' and \ -> \\ , so an escaped apostrophe
		// (\') inside a single-quoted region must NOT toggle the quote state (otherwise the
		// region closes early and JSON braces inside get miscounted, collapsing the expression).
		if (inDoubleQuotes || inSingleQuotes) && c == '\\' && i+1 < len(text) {
			i++ // skip escaped char (\" inside "", or \' / \\ inside '')
			continue
		}
		if c == '\'' && !inDoubleQuotes {
			inSingleQuotes = !inSingleQuotes
			continue
		}
		if c == '"' && !inSingleQuotes {
			inDoubleQuotes = !inDoubleQuotes
			continue
		}
		if inSingleQuotes || inDoubleQuotes {
			continue
		}
		if i < len(text)-1 && c == '{' && text[i+1] == '{' {
			depth++
			i++
		} else if i < len(text)-1 && c == '}' && text[i+1] == '}' {
			depth--
			if depth == 0 {
				end := i + 2
				if debugExpressionBraces && len(text) > start+4 {
					inner := text[start+2 : end-2]
					if strings.HasPrefix(inner, "CONDITION:") {
						beforeLen := 50
						if len(text)-start < beforeLen {
							beforeLen = len(text) - start
						}
						afterStart := end - 25
						if afterStart < 0 {
							afterStart = 0
						}
						afterEnd := end + 20
						if afterEnd > len(text) {
							afterEnd = len(text)
						}
						fmt.Printf("[BRACE_DEBUG] findMatch start=%d end=%d len(inner)=%d\n  before: ...%q\n  after:  %q...\n",
							start, end, len(inner), text[start:start+beforeLen], text[afterStart:afterEnd])
					}
				}
				return end
			}
			i++
		}
	}
	return -1
}

// processExpressionPlaceholders handles {{...}} expressions.
// Processes innermost-first: finds the first {{...}} that does not contain any
// other {{ (a leaf expression), evaluates it, replaces it, and repeats. This
// avoids relying on matching the outermost }} in deeply nested CONDITION/ARRAY
// expressions and fixes standard_employment_type__c-style configs.
func (ep *ExpressionProcessor) processExpressionPlaceholders(ctx context.Context, text string, masterDTO *common_dto.MasterDTO, serviceNameMap map[string]*models.EsaLog) string {
	for iteration := 0; ; iteration++ {
		// Honor the request-scoped deadline. This is the single most important guard: each iteration
		// resolves one leaf {{...}} and re-scans the (possibly multi-MB) text, so bailing here bounds any
		// runaway to the overall request budget instead of spinning CPU-bound indefinitely. Returns
		// best-effort text; the caller aborts via ctx.Err() before any network call.
		if ctxErr(ctx) != nil {
			return text
		}
		start := strings.Index(text, "{{")
		if start == -1 {
			break
		}
		replaced := false
		// Find innermost: walk into nested {{ until we have a segment with no {{ inside
		for {
			end := ep.findMatchingClosingDoubleBraces(text, start)
			if end == -1 {
				if debugPipeline {
					fmt.Printf("[EXPR_PROC] findMatchingClosingDoubleBraces FAILED at start=%d, text around start: %s\n",
						start, debugTruncate(text[start:], 200))
				}
				// Matching }} not found (e.g. braces inside quoted JSON). Drill to first inner {{ and try that.
				innerStart := strings.Index(text[start+2:], "{{")
				if innerStart != -1 {
					if debugPipeline {
						fmt.Printf("[EXPR_PROC] Drilling to inner {{ at offset %d\n", start+2+innerStart)
					}
					start = start + 2 + innerStart
					continue
				}
				// No inner {{ — use last }} as fallback so we still evaluate this segment (avoids leaving outer CONDITION unevaluated).
				if lastClose := strings.LastIndex(text[start+2:], "}}"); lastClose >= 0 {
					end = start + 2 + lastClose + 2
					if debugPipeline {
						fmt.Printf("[EXPR_PROC] Using LastIndex fallback, end=%d\n", end)
					}
				}
				if end == -1 {
					if debugPipeline {
						fmt.Printf("[EXPR_PROC] No fallback found, breaking.\n")
					}
					break
				}
			}
			inner := text[start+2 : end-2]
			if !strings.Contains(inner, "{{") {
				// Leaf expression — evaluate and replace
				result := ep.evaluateExpression(inner, masterDTO, serviceNameMap)
				if debugPipeline {
					exprType := "UNKNOWN"
					if idx := strings.Index(inner, ":"); idx > 0 && idx < 20 {
						exprType = inner[:idx]
					}
					fmt.Printf("[EXPR_PROC] LEAF iter=%d type=%s start=%d end=%d\n  inner: %s\n  result: %s\n",
						iteration, exprType, start, end, debugTruncate(inner, 300), debugTruncate(result, 300))
				}
				if debugExpressionBraces && strings.HasPrefix(inner, "CONDITION:") {
					innerLen := 80
					if len(inner) < innerLen {
						innerLen = len(inner)
					}
					fmt.Printf("[BRACE_DEBUG] REPLACE leaf CONDITION start=%d end=%d result=%q\n  inner(80)=%q\n",
						start, end, result, inner[:innerLen])
				}
				// If result contains ':', wrap in single quotes (and escape quotes) so the next
				// CUSTOM parse (e.g. getJsonPath:PARAM:output) does not split on colons inside PARAM.
				// Only wrap when this replacement is not the entire output (ignoring surrounding whitespace).
				// Otherwise final output would be quoted and cleanupTypeMarkers would not strip __NUMERIC_INT__:
				// / __ARRAY_PARSE__: etc., breaking configs that expect raw values.
				isEntireOutput := strings.TrimSpace(text[:start]) == "" && strings.TrimSpace(text[end:]) == ""
				if strings.Contains(result, ":") && !isEntireOutput {
					escaped := strings.ReplaceAll(result, "\\", "\\\\")
					escaped = strings.ReplaceAll(escaped, "'", "\\'")
					result = "'" + escaped + "'"
				}
				text = text[:start] + result + text[end:]
				replaced = true
				break
			}
			// Content has nested {{ — process the first inner one next
			start = start + 2 + strings.Index(inner, "{{")
		}
		if !replaced {
			if debugPipeline {
				fmt.Printf("[EXPR_PROC] No replacement made, exiting loop. Remaining text: %s\n", debugTruncate(text, 300))
			}
			break
		}
	}
	return text
}

// evaluateExpression evaluates different types of expressions
func (ep *ExpressionProcessor) evaluateExpression(expression string, masterDTO *common_dto.MasterDTO, serviceNameMap map[string]*models.EsaLog) string {
	expression = strings.TrimSpace(expression)

	// Handle different expression types
	switch {
	case strings.HasPrefix(expression, "CALC:"):
		return ep.evaluateCalculationExpression(expression[5:], masterDTO, serviceNameMap)
	case strings.HasPrefix(expression, "FORMAT:"):
		return ep.evaluateFormattingExpression(expression[7:], masterDTO, serviceNameMap)
	case strings.HasPrefix(expression, "TRANSFORM:"):
		return ep.evaluateTransformationExpression(expression[10:], masterDTO, serviceNameMap)
	case strings.HasPrefix(expression, "CONCAT:"):
		return ep.evaluateConcatenationExpression(expression[7:], masterDTO, serviceNameMap)
	case strings.HasPrefix(expression, "CONDITION:"):
		return ep.evaluateConditionalExpression(expression[10:], masterDTO, serviceNameMap)
	case strings.HasPrefix(expression, "CUSTOM:"):
		return ep.evaluateCustomExpression(expression[7:], masterDTO, serviceNameMap)
	case strings.HasPrefix(expression, "NUMERIC:"):
		return ep.evaluateNumericExpression(expression[8:], masterDTO, serviceNameMap)
	case strings.HasPrefix(expression, "BOOLEAN:"):
		return ep.evaluateBooleanExpression(expression[8:], masterDTO, serviceNameMap)
	case strings.HasPrefix(expression, "JSON:"):
		return ep.evaluateJsonExpression(expression[5:], masterDTO, serviceNameMap)
	case strings.HasPrefix(expression, "ARRAY:"):
		return ep.evaluateArrayExpression(expression[6:], masterDTO, serviceNameMap)
	case strings.HasPrefix(expression, "CONDITIONAL:"):
		return ep.evaluateConditionalLogicExpression(expression[12:], masterDTO, serviceNameMap)
	case strings.Contains(expression, "+") || strings.Contains(expression, "-") ||
		strings.Contains(expression, "*") || strings.Contains(expression, "/") ||
		strings.Contains(expression, "%"):
		return ep.evaluateSimpleArithmetic(expression, masterDTO, serviceNameMap)
	case ep.isTopLevelFallback(expression):
		return ep.evaluateExpressionFallback(expression, masterDTO, serviceNameMap)
	default:
		return ep.evaluateSimpleExpression(expression, masterDTO, serviceNameMap)
	}
}

// ============================================================================
// CALCULATION EXPRESSIONS {{CALC:...}}
// ============================================================================

// evaluateCalculationExpression handles arithmetic calculations with advanced math functions
// and grouping parentheses.
//   - A pure single function call (e.g. ROUND(x,2)) is routed to evaluateAdvancedMathFunction so its
//     exact behavior and formatting are preserved unchanged.
//   - Anything else containing parentheses (grouping like a*(b+c), or a function call mixed with
//     operators like ROUND(x,2)*12) is handled by the recursive-descent evaluator.
//   - Expressions without parentheses use the flat multi-operand evaluator.
func (ep *ExpressionProcessor) evaluateCalculationExpression(expression string, masterDTO *common_dto.MasterDTO, serviceNameMap map[string]*models.EsaLog) string {
	expression = strings.TrimSpace(expression)

	if strings.Contains(expression, "(") {
		if isPureMathFunctionCall(expression) {
			// Preserve legacy per-function formatting exactly.
			return ep.evaluateAdvancedMathFunction(expression, masterDTO, serviceNameMap)
		}
		return ep.evalArithmeticFormatted(expression, masterDTO, serviceNameMap)
	}

	// Paren-free CALC arithmetic: multi-operand with precedence.
	return ep.evaluateCalcArithmetic(expression, masterDTO, serviceNameMap)
}

// evaluateAdvancedMathFunction handles advanced math functions like ROUND, CEIL, etc.
func (ep *ExpressionProcessor) evaluateAdvancedMathFunction(expression string, masterDTO *common_dto.MasterDTO, serviceNameMap map[string]*models.EsaLog) string {
	// Parse function calls like ROUND(value, precision) or MAX(val1, val2, val3)
	parenIndex := strings.Index(expression, "(")
	if parenIndex == -1 {
		return ep.evaluateSimpleArithmetic(expression, masterDTO, serviceNameMap)
	}

	funcName := strings.TrimSpace(expression[:parenIndex])
	argsStr := strings.TrimSpace(expression[parenIndex+1:])
	if !strings.HasSuffix(argsStr, ")") {
		return ""
	}
	argsStr = argsStr[:len(argsStr)-1] // Remove closing parenthesis

	// Parse arguments
	args := ep.parseCommaSeparatedArgs(argsStr)
	resolvedArgs := make([]float64, len(args))

	for i, arg := range args {
		// Use unified parameter processing for consistent fallback support
		resolvedValue := ep.processUnifiedParameter(arg, masterDTO, serviceNameMap)
		val, err := strconv.ParseFloat(resolvedValue, 64)
		if err != nil {
			return ""
		}
		resolvedArgs[i] = val
	}

	// Execute function
	switch strings.ToUpper(funcName) {
	case "ROUND":
		if len(resolvedArgs) >= 1 {
			precision := 0
			if len(resolvedArgs) >= 2 {
				precision = int(resolvedArgs[1])
			}
			multiplier := math.Pow(10, float64(precision))
			result := math.Round(resolvedArgs[0]*multiplier) / multiplier
			return fmt.Sprintf("%.*f", precision, result)
		}
	case "CEIL":
		if len(resolvedArgs) >= 1 {
			return fmt.Sprintf("%.0f", math.Ceil(resolvedArgs[0]))
		}
	case "FLOOR":
		if len(resolvedArgs) >= 1 {
			return fmt.Sprintf("%.0f", math.Floor(resolvedArgs[0]))
		}
	case "ABS":
		if len(resolvedArgs) >= 1 {
			return fmt.Sprintf("%.2f", math.Abs(resolvedArgs[0]))
		}
	case "MAX":
		if len(resolvedArgs) >= 1 {
			max := resolvedArgs[0]
			for _, val := range resolvedArgs[1:] {
				if val > max {
					max = val
				}
			}
			return fmt.Sprintf("%.2f", max)
		}
	case "MIN":
		if len(resolvedArgs) >= 1 {
			min := resolvedArgs[0]
			for _, val := range resolvedArgs[1:] {
				if val < min {
					min = val
				}
			}
			return fmt.Sprintf("%.2f", min)
		}
	case "PERCENTAGE":
		if len(resolvedArgs) >= 2 {
			if resolvedArgs[1] != 0 {
				result := (resolvedArgs[0] / resolvedArgs[1]) * 100
				return fmt.Sprintf("%.2f", result)
			}
		}
	case "RANDOM":
		if len(resolvedArgs) >= 2 {
			min := int(resolvedArgs[0])
			max := int(resolvedArgs[1])
			if max > min {
				result := rand.Intn(max-min+1) + min
				return fmt.Sprintf("%d", result)
			}
		}
	case "POWER":
		if len(resolvedArgs) >= 2 {
			result := math.Pow(resolvedArgs[0], resolvedArgs[1])
			return fmt.Sprintf("%.2f", result)
		}
	case "SQRT":
		if len(resolvedArgs) >= 1 {
			if resolvedArgs[0] >= 0 {
				result := math.Sqrt(resolvedArgs[0])
				return fmt.Sprintf("%.2f", result)
			}
		}
	case "LOG":
		if len(resolvedArgs) >= 1 {
			base := 10.0
			if len(resolvedArgs) >= 2 {
				base = resolvedArgs[1]
			}
			if resolvedArgs[0] > 0 && base > 0 && base != 1 {
				result := math.Log(resolvedArgs[0]) / math.Log(base)
				return fmt.Sprintf("%.2f", result)
			}
		}
	case "MOD":
		if len(resolvedArgs) >= 2 {
			if resolvedArgs[1] != 0 {
				result := math.Mod(resolvedArgs[0], resolvedArgs[1])
				return fmt.Sprintf("%.2f", result)
			}
		}
	}

	return ""
}

// evaluateSimpleArithmetic evaluates a two-operand arithmetic expression (legacy behavior). This
// is the general (non-CALC) path reached from evaluateExpression for any string containing an
// operator. It deliberately handles ONLY a single operator with exactly two operands so that
// dash-bearing identifiers (e.g. "1234-5678-9012") are left as "" rather than coerced to a number.
// Multi-operand precedence and grouping parentheses are available via {{CALC:...}} (see
// evaluateCalcArithmetic / evalArithmeticFormatted).
func (ep *ExpressionProcessor) evaluateSimpleArithmetic(expression string, masterDTO *common_dto.MasterDTO, serviceNameMap map[string]*models.EsaLog) string {
	expression = strings.TrimSpace(expression)

	operators := []string{"+", "-", "*", "/", "%"}
	for _, op := range operators {
		if strings.Contains(expression, op) {
			parts := strings.Split(expression, op)
			if len(parts) == 2 {
				left := ep.getNumericValue(strings.TrimSpace(parts[0]), masterDTO, serviceNameMap)
				right := ep.getNumericValue(strings.TrimSpace(parts[1]), masterDTO, serviceNameMap)

				var result float64
				switch op {
				case "+":
					result = left + right
				case "-":
					result = left - right
				case "*":
					result = left * right
				case "/":
					if right == 0 {
						return "0"
					}
					result = left / right
				case "%":
					if int(right) == 0 {
						return "0"
					}
					result = float64(int(left) % int(right))
				}

				if result == float64(int(result)) {
					return fmt.Sprintf("%d", int(result))
				}
				return fmt.Sprintf("%.2f", result)
			}
			break
		}
	}

	return ""
}

// evaluateCalcArithmetic evaluates a paren-free {{CALC:...}} arithmetic expression with
// multi-operand support and operator precedence (* / % before + -, left-to-right). Operands are
// resolved via getNumericValue (missing/non-numeric -> 0). Division/modulo by zero yields "0".
// A whole-number result is returned as an integer, otherwise with two decimals.
func (ep *ExpressionProcessor) evaluateCalcArithmetic(expression string, masterDTO *common_dto.MasterDTO, serviceNameMap map[string]*models.EsaLog) string {
	expression = strings.TrimSpace(expression)
	if expression == "" {
		return ""
	}

	// Guard: a full ISO date literal (e.g. "2024-01-15") must not be split into 2024-1-15. Only
	// explicit year-first date/datetime layouts count as dates — slash forms like "10/2" are NOT
	// (they are valid division) and partials like "2024-01" are treated as arithmetic.
	if looksLikeStrictDateLiteral(expression) {
		return ""
	}

	operands, ops, ok := ep.tokenizeArithmetic(expression)
	if !ok || len(operands) < 2 || len(ops) != len(operands)-1 {
		return ""
	}

	values := make([]float64, len(operands))
	for i, operand := range operands {
		values[i] = ep.getNumericValue(strings.TrimSpace(operand), masterDTO, serviceNameMap)
	}

	result, ok := evalArithmeticWithPrecedence(values, ops)
	if !ok {
		return "0" // division / modulo by zero
	}
	return formatArithmeticResult(result)
}

// looksLikeStrictDateLiteral reports whether s is a full date/datetime literal in an explicit
// year-first layout, so it must not be treated as arithmetic. Slash date forms are deliberately
// excluded because they collide with division (e.g. "10/2"), and partials (e.g. "2024-01") are not
// matched so year-month subtraction still evaluates.
func looksLikeStrictDateLiteral(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return false
	}
	layouts := []string{
		"2006-01-02T15:04:05.000-0700",
		"2006-01-02T15:04:05-0700",
		"2006-01-02T15:04:05.000Z",
		"2006-01-02T15:04:05Z",
		"2006-01-02T15:04:05-07:00",
		"2006-01-02T15:04:05",
		"2006-01-02 15:04:05",
		"2006-01-02",
	}
	for _, l := range layouts {
		if _, err := time.Parse(l, s); err == nil {
			return true
		}
	}
	return false
}

// tokenizeArithmetic splits an arithmetic expression into operands and top-level operators.
// Operators inside <...> (DB placeholders), (...) (service placeholders), {{...}} (nested
// expressions), or quotes are NOT treated as separators, so placeholders/literals stay intact.
// A leading '+'/'-' (at the start or immediately after another operator) is treated as a unary sign
// and kept with the following operand. Returns ok=false if the token structure is malformed.
func (ep *ExpressionProcessor) tokenizeArithmetic(expression string) (operands []string, ops []byte, ok bool) {
	var current strings.Builder
	angleDepth, parenDepth, braceDepth := 0, 0, 0
	inQuotes := false
	var quoteChar byte
	expectOperand := true // start of expression expects an operand (or unary sign)

	for i := 0; i < len(expression); i++ {
		c := expression[i]

		if inQuotes {
			if c == '\\' && i+1 < len(expression) {
				current.WriteByte(c)
				i++
				current.WriteByte(expression[i])
				continue
			}
			current.WriteByte(c)
			if c == quoteChar {
				inQuotes = false
			}
			continue
		}

		switch c {
		case '\'', '"':
			inQuotes = true
			quoteChar = c
			current.WriteByte(c)
			expectOperand = false
		case '<':
			angleDepth++
			current.WriteByte(c)
			expectOperand = false
		case '>':
			if angleDepth > 0 {
				angleDepth--
			}
			current.WriteByte(c)
		case '(':
			parenDepth++
			current.WriteByte(c)
			expectOperand = false
		case ')':
			if parenDepth > 0 {
				parenDepth--
			}
			current.WriteByte(c)
		case '{':
			if i+1 < len(expression) && expression[i+1] == '{' {
				braceDepth++
				current.WriteString("{{")
				i++
			} else {
				current.WriteByte(c)
			}
			expectOperand = false
		case '}':
			if i+1 < len(expression) && expression[i+1] == '}' {
				if braceDepth > 0 {
					braceDepth--
				}
				current.WriteString("}}")
				i++
			} else {
				current.WriteByte(c)
			}
		case '+', '-', '*', '/', '%':
			atTopLevel := angleDepth == 0 && parenDepth == 0 && braceDepth == 0
			if !atTopLevel {
				current.WriteByte(c)
				break
			}
			if expectOperand {
				// Unary sign: only + / - are valid here; keep it with the operand.
				if c == '+' || c == '-' {
					current.WriteByte(c)
					expectOperand = false
					continue
				}
				return nil, nil, false // '*' '/' '%' with no left operand
			}
			operands = append(operands, current.String())
			current.Reset()
			ops = append(ops, c)
			expectOperand = true
		default:
			current.WriteByte(c)
			if c != ' ' && c != '\t' {
				expectOperand = false
			}
		}
	}

	operands = append(operands, current.String())
	if inQuotes || angleDepth != 0 || parenDepth != 0 || braceDepth != 0 {
		return nil, nil, false
	}
	if len(operands) != len(ops)+1 {
		return nil, nil, false
	}
	return operands, ops, true
}

// evalArithmeticWithPrecedence evaluates values/ops honoring precedence: first pass folds
// * / % left-to-right, second pass folds + -. Returns ok=false on division/modulo by zero.
func evalArithmeticWithPrecedence(values []float64, ops []byte) (float64, bool) {
	if len(values) == 0 {
		return 0, false
	}
	// Pass 1: high precedence (* / %)
	reduced := []float64{values[0]}
	lowOps := []byte{}
	for i, op := range ops {
		switch op {
		case '*':
			reduced[len(reduced)-1] *= values[i+1]
		case '/':
			if values[i+1] == 0 {
				return 0, false
			}
			reduced[len(reduced)-1] /= values[i+1]
		case '%':
			if int64(values[i+1]) == 0 {
				return 0, false
			}
			reduced[len(reduced)-1] = float64(int64(reduced[len(reduced)-1]) % int64(values[i+1]))
		default: // '+' or '-'
			reduced = append(reduced, values[i+1])
			lowOps = append(lowOps, op)
		}
	}
	// Pass 2: low precedence (+ -)
	result := reduced[0]
	for i, op := range lowOps {
		if op == '+' {
			result += reduced[i+1]
		} else {
			result -= reduced[i+1]
		}
	}
	return result, true
}

// mathFunctionNames is the set of function names recognized by evaluateAdvancedMathFunction.
// Used to distinguish a function call NAME(...) from grouping parentheses (...) and from a
// service placeholder ((...)).
var mathFunctionNames = map[string]bool{
	"ROUND": true, "CEIL": true, "FLOOR": true, "ABS": true, "MAX": true, "MIN": true,
	"PERCENTAGE": true, "RANDOM": true, "POWER": true, "SQRT": true, "LOG": true, "MOD": true,
}

// isPureMathFunctionCall reports whether expr is exactly one function call NAME(...) spanning the
// whole expression (e.g. "ROUND(x,2)"), so it can be routed to evaluateAdvancedMathFunction with
// its legacy formatting preserved. Returns false for grouping or mixed expressions like
// "ROUND(x,2)*12" or "2*(3+4)".
func isPureMathFunctionCall(expr string) bool {
	expr = strings.TrimSpace(expr)
	open := strings.Index(expr, "(")
	if open < 0 || !strings.HasSuffix(expr, ")") {
		return false
	}
	if !mathFunctionNames[strings.ToUpper(strings.TrimSpace(expr[:open]))] {
		return false
	}
	// The '(' at open must match the final ')'.
	depth := 0
	for i := open; i < len(expr); i++ {
		switch expr[i] {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return i == len(expr)-1
			}
		}
	}
	return false
}

// isMathFunctionCallToken reports whether token is a function call NAME(...) for a recognized
// math function (used when a primary is a nested/embedded function call).
func isMathFunctionCallToken(token string) bool {
	token = strings.TrimSpace(token)
	open := strings.Index(token, "(")
	if open <= 0 || !strings.HasSuffix(token, ")") {
		return false
	}
	return mathFunctionNames[strings.ToUpper(strings.TrimSpace(token[:open]))]
}

// formatArithmeticResult renders a numeric result as an integer when it is a whole number,
// otherwise with two decimals (matching the flat arithmetic evaluator).
func formatArithmeticResult(v float64) string {
	if v == float64(int64(v)) {
		return fmt.Sprintf("%d", int64(v))
	}
	return fmt.Sprintf("%.2f", v)
}

// arithmeticEvaluator is a recursive-descent evaluator for CALC arithmetic that supports grouping
// parentheses, operator precedence (* / % before + -), unary +/-, and embedded function calls.
// Operands (numbers and placeholders <...>, ((...)), {{...}}) are resolved via getNumericValue;
// function-call primaries delegate to evaluateAdvancedMathFunction so their behavior is identical.
type arithmeticEvaluator struct {
	ep             *ExpressionProcessor
	masterDTO      *common_dto.MasterDTO
	serviceNameMap map[string]*models.EsaLog
	s              string
	pos            int
	failed         bool // true on any parse error
	divByZero      bool // true specifically on division / modulo by zero (legacy returns "0")
}

func (p *arithmeticEvaluator) skipSpaces() {
	for p.pos < len(p.s) && (p.s[p.pos] == ' ' || p.s[p.pos] == '\t') {
		p.pos++
	}
}

// parseExpr handles the lowest precedence operators (+ and -).
func (p *arithmeticEvaluator) parseExpr() float64 {
	v := p.parseTerm()
	for {
		p.skipSpaces()
		if p.failed || p.pos >= len(p.s) {
			break
		}
		op := p.s[p.pos]
		if op != '+' && op != '-' {
			break
		}
		p.pos++
		r := p.parseTerm()
		if op == '+' {
			v += r
		} else {
			v -= r
		}
	}
	return v
}

// parseTerm handles the higher precedence operators (* / %).
func (p *arithmeticEvaluator) parseTerm() float64 {
	v := p.parseFactor()
	for {
		p.skipSpaces()
		if p.failed || p.pos >= len(p.s) {
			break
		}
		op := p.s[p.pos]
		if op != '*' && op != '/' && op != '%' {
			break
		}
		p.pos++
		r := p.parseFactor()
		switch op {
		case '*':
			v *= r
		case '/':
			if r == 0 {
				p.failed, p.divByZero = true, true
				return 0
			}
			v /= r
		case '%':
			if int64(r) == 0 {
				p.failed, p.divByZero = true, true
				return 0
			}
			v = float64(int64(v) % int64(r))
		}
	}
	return v
}

// parseFactor handles a unary sign, a grouping subexpression, or a primary operand.
func (p *arithmeticEvaluator) parseFactor() float64 {
	p.skipSpaces()
	if p.pos >= len(p.s) {
		p.failed = true
		return 0
	}
	c := p.s[p.pos]

	if c == '+' || c == '-' {
		p.pos++
		v := p.parseFactor()
		if c == '-' {
			return -v
		}
		return v
	}

	if c == '(' {
		// "((" is ambiguous: a service placeholder ((path)) operand OR nested grouping ((a+b)).
		// Treat it as a placeholder operand only when it parses as a plausible placeholder;
		// otherwise fall through to grouping, consuming a single '('.
		if p.pos+1 < len(p.s) && p.s[p.pos+1] == '(' && p.servicePlaceholderEnd(p.pos) > 0 {
			return p.parsePrimary()
		}
		p.pos++ // consume '('
		v := p.parseExpr()
		p.skipSpaces()
		if p.pos < len(p.s) && p.s[p.pos] == ')' {
			p.pos++
		} else {
			p.failed = true
		}
		return v
	}

	return p.parsePrimary()
}

// servicePlaceholderEnd reports the end index (exclusive) of a service placeholder ((path)) that
// starts at pos, or -1 if the "((...))" at pos is not a plausible placeholder (e.g. it is really
// nested grouping like ((a+b))). A placeholder's inner content starts with a letter/underscore and
// contains no top-level arithmetic operators.
func (p *arithmeticEvaluator) servicePlaceholderEnd(pos int) int {
	if !(pos+1 < len(p.s) && p.s[pos] == '(' && p.s[pos+1] == '(') {
		return -1
	}
	// Find the matching close, skipping quoted regions so a literal paren inside a filter value
	// (e.g. [name=='A(B)']) does not miscount the depth.
	depth := 0
	inQuotes := false
	var qc byte
	i := pos
	for ; i < len(p.s); i++ {
		c := p.s[i]
		if inQuotes {
			if c == qc {
				inQuotes = false
			}
			continue
		}
		switch c {
		case '\'', '"':
			inQuotes = true
			qc = c
		case '(':
			depth++
		case ')':
			depth--
		}
		if !inQuotes && depth == 0 {
			break
		}
	}
	if i >= len(p.s) || i < 1 || p.s[i-1] != ')' {
		return -1 // unbalanced or not closed by "))"
	}
	inner := strings.TrimSpace(p.s[pos+2 : i-1])
	if inner == "" {
		return -1
	}
	c0 := inner[0]
	isNameStart := c0 == '_' || (c0 >= 'A' && c0 <= 'Z') || (c0 >= 'a' && c0 <= 'z')
	// Only a TOP-LEVEL arithmetic operator (outside [...] filters and quotes) indicates grouping
	// like ((a+b)); operators inside a filter value (e.g. [CODE=='A+B']) do not disqualify it.
	if !isNameStart || hasTopLevelArithmeticOperator(inner) {
		return -1
	}
	return i + 1
}

// hasTopLevelArithmeticOperator reports whether s contains an arithmetic operator (+ - * / %) at
// the top level — outside any [...] bracket filter and outside quoted regions.
func hasTopLevelArithmeticOperator(s string) bool {
	bracket := 0
	inQuotes := false
	var qc byte
	for i := 0; i < len(s); i++ {
		c := s[i]
		if inQuotes {
			if c == qc {
				inQuotes = false
			}
			continue
		}
		switch c {
		case '\'', '"':
			inQuotes = true
			qc = c
		case '[':
			bracket++
		case ']':
			if bracket > 0 {
				bracket--
			}
		case '+', '-', '*', '/', '%':
			if bracket == 0 {
				return true
			}
		}
	}
	return false
}

// parsePrimary reads a single operand token — a number, a placeholder (<...>, ((...)), {{...}}),
// or an embedded function call NAME(...) — respecting nested depth, and stopping at a top-level
// operator or a closing ')' of an enclosing group. Function-call tokens delegate to
// evaluateAdvancedMathFunction; all other tokens go through getNumericValue.
func (p *arithmeticEvaluator) parsePrimary() float64 {
	start := p.pos
	angle, brace, paren := 0, 0, 0
	for p.pos < len(p.s) {
		c := p.s[p.pos]
		atTop := angle == 0 && brace == 0 && paren == 0
		if atTop && (c == '+' || c == '-' || c == '*' || c == '/' || c == '%' || c == ')') {
			break
		}
		switch {
		case c == '<':
			angle++
		case c == '>':
			if angle > 0 {
				angle--
			}
		case c == '{' && p.pos+1 < len(p.s) && p.s[p.pos+1] == '{':
			brace++
			p.pos++
		case c == '}' && p.pos+1 < len(p.s) && p.s[p.pos+1] == '}':
			if brace > 0 {
				brace--
			}
			p.pos++
		case c == '(':
			paren++
		case c == ')':
			// Only reached when paren > 0 (a top-level ')' breaks the loop above).
			paren--
		}
		p.pos++
	}
	token := strings.TrimSpace(p.s[start:p.pos])
	if token == "" {
		p.failed = true
		return 0
	}

	if isMathFunctionCallToken(token) {
		res := ep_evalAdvanced(p, token)
		f, err := strconv.ParseFloat(strings.TrimSpace(res), 64)
		if err != nil {
			p.failed = true
			return 0
		}
		return f
	}
	// A NAME(...) token whose NAME is not a recognized function is not valid arithmetic. Fail so the
	// whole expression yields "" (matching the legacy advanced-math path) instead of coercing to 0.
	if looksLikeFunctionCallToken(token) {
		p.failed = true
		return 0
	}

	resolved := p.ep.getNumericValue(token, p.masterDTO, p.serviceNameMap)
	return resolved
}

// looksLikeFunctionCallToken reports whether token has the shape NAME(...) with an alphabetic NAME,
// regardless of whether NAME is a recognized math function.
func looksLikeFunctionCallToken(token string) bool {
	token = strings.TrimSpace(token)
	open := strings.Index(token, "(")
	if open <= 0 || !strings.HasSuffix(token, ")") {
		return false
	}
	for i := 0; i < open; i++ {
		c := token[i]
		if !((c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z')) {
			return false
		}
	}
	return true
}

// ep_evalAdvanced delegates a function-call token to evaluateAdvancedMathFunction (kept as a tiny
// helper so parsePrimary stays readable).
func ep_evalAdvanced(p *arithmeticEvaluator, token string) string {
	return p.ep.evaluateAdvancedMathFunction(token, p.masterDTO, p.serviceNameMap)
}

// evalArithmeticFormatted evaluates an arithmetic expression that contains parentheses (grouping,
// or a function call mixed with operators) and formats the result. Returns "0" on division/modulo
// by zero (legacy behavior) and "" if the expression is not fully parseable as arithmetic.
func (ep *ExpressionProcessor) evalArithmeticFormatted(expression string, masterDTO *common_dto.MasterDTO, serviceNameMap map[string]*models.EsaLog) string {
	p := &arithmeticEvaluator{ep: ep, masterDTO: masterDTO, serviceNameMap: serviceNameMap, s: expression}
	v := p.parseExpr()
	p.skipSpaces()
	if p.divByZero {
		return "0"
	}
	if p.failed || p.pos != len(p.s) {
		return "" // not a fully valid arithmetic expression
	}
	return formatArithmeticResult(v)
}

// ============================================================================
// FORMATTING EXPRESSIONS {{FORMAT:...}}
// ============================================================================

// formatDatePatternSeparator separates value from pattern in FORMAT:date when the
// output pattern contains colons (e.g. YYYY-MM-DD HH:mm:ss). Use: date:value::pattern.
const formatDatePatternSeparator = "::"

// evaluateFormattingExpression handles comprehensive data formatting
func (ep *ExpressionProcessor) evaluateFormattingExpression(expression string, masterDTO *common_dto.MasterDTO, serviceNameMap map[string]*models.EsaLog) string {
	parts := strings.Split(expression, ":")
	if len(parts) < 3 {
		return ""
	}

	formatType := strings.TrimSpace(parts[0])
	var fieldPath, formatPattern string
	if strings.ToLower(formatType) == "date" && len(parts) >= 3 {
		if idx := strings.Index(expression, formatDatePatternSeparator); idx >= 0 {
			// Pattern contains colons: value is before "::", pattern is after (e.g. date:value::YYYY-MM-DD HH:mm:ss)
			fieldPath = strings.TrimSpace(expression[5:idx]) // after "date:"
			formatPattern = strings.TrimSpace(expression[idx+len(formatDatePatternSeparator):])
		} else {
			// Date value may contain colons; pattern is last segment
			formatPattern = strings.TrimSpace(parts[len(parts)-1])
			fieldPath = strings.TrimSpace(strings.Join(parts[1:len(parts)-1], ":"))
		}
	} else {
		fieldPath = strings.TrimSpace(parts[1])
		formatPattern = strings.TrimSpace(parts[2])
	}

	// Use unified parameter processing for consistent nested expression support
	fieldValue := ep.processUnifiedParameter(fieldPath, masterDTO, serviceNameMap)
	if fieldValue == "" {
		return ""
	}

	switch strings.ToLower(formatType) {
	case "date":
		return ep.formatDate(fieldValue, formatPattern)
	case "number":
		// Handle currency formatting: FORMAT:number:50000:currency:USD
		if strings.ToLower(formatPattern) == "currency" && len(parts) >= 4 {
			currency := strings.ToUpper(strings.TrimSpace(parts[3]))
			return ep.formatCurrency(fieldValue, currency)
		}
		return ep.formatNumberValue(fieldValue, formatPattern)
	case "text":
		return ep.formatText(fieldValue, formatPattern)
	case "currency":
		return ep.formatCurrency(fieldValue, formatPattern)
	default:
		return fieldValue
	}
}

// formatDate formats date strings with comprehensive pattern support.
// Empty or whitespace-only value returns "" (no default to current time).
func (ep *ExpressionProcessor) formatDate(value, pattern string) string {
	if strings.TrimSpace(value) == "" {
		return ""
	}
	parsedTime, ok := parseStandardDateTime(value)
	if !ok {
		return value // Return original if can't parse
	}

	// Format according to pattern
	switch pattern {
	case "DD-MM-YYYY":
		return parsedTime.Format("02-01-2006")
	case "YYYY-MM-DD":
		return parsedTime.Format("2006-01-02")
	case "MM/DD/YYYY":
		return parsedTime.Format("01/02/2006")
	case "DD/MM/YYYY":
		return parsedTime.Format("02/01/2006")
	case "YYYY-MM-DD HH:mm:ss":
		return parsedTime.Format("2006-01-02 15:04:05")
	case "RFC3339":
		return parsedTime.Format(time.RFC3339)
	case "Unix":
		return fmt.Sprintf("%d", parsedTime.Unix())
	default:
		// Use translateDateFormat so all §10.2 patterns (DDMMYYYY, YYYYMMDD, etc.) work
		layout := ep.translateDateFormat(pattern)
		return parsedTime.Format(layout)
	}
}

// formatNumberValue formats numeric values with comprehensive pattern support
func (ep *ExpressionProcessor) formatNumberValue(value, pattern string) string {
	val, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return value // Return original if can't parse
	}

	switch pattern {
	case "integer":
		return fmt.Sprintf("%.0f", val)
	case "0decimal":
		return fmt.Sprintf("%.0f", val)
	case "1decimal":
		return fmt.Sprintf("%.1f", val)
	case "2decimal":
		return fmt.Sprintf("%.2f", val)
	case "3decimal":
		return fmt.Sprintf("%.3f", val)
	case "4decimal":
		return fmt.Sprintf("%.4f", val)
	case "5decimal":
		return fmt.Sprintf("%.5f", val)
	case "6decimal":
		return fmt.Sprintf("%.6f", val)
	case "7decimal":
		return fmt.Sprintf("%.7f", val)
	case "8decimal":
		return fmt.Sprintf("%.8f", val)
	case "9decimal":
		return fmt.Sprintf("%.9f", val)
	case "10decimal":
		return fmt.Sprintf("%.10f", val)
	default:
		return value // Return original for unknown patterns
	}
}

// formatCurrency formats currency values with comprehensive currency support
func (ep *ExpressionProcessor) formatCurrency(value, pattern string) string {
	val, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return value // Return original if can't parse
	}

	// Format with commas
	formattedNumber := ep.addCommasToNumber(val)

	switch pattern {
	case "INR":
		return fmt.Sprintf("₹%s", formattedNumber)
	case "USD":
		return fmt.Sprintf("$%s", formattedNumber)
	case "EUR":
		return fmt.Sprintf("€%s", formattedNumber)
	case "GBP":
		return fmt.Sprintf("£%s", formattedNumber)
	case "JPY":
		return fmt.Sprintf("¥%.0f", val) // JPY typically has no decimals
	case "CNY":
		return fmt.Sprintf("¥%s", formattedNumber)
	case "AUD":
		return fmt.Sprintf("A$%s", formattedNumber)
	case "CAD":
		return fmt.Sprintf("C$%s", formattedNumber)
	default:
		return value // Return original for unknown currencies
	}
}

// formatText formats text values with various transformations
func (ep *ExpressionProcessor) formatText(value, pattern string) string {
	switch strings.ToLower(pattern) {
	case "uppercase", "upper":
		return strings.ToUpper(value)
	case "lowercase", "lower":
		return strings.ToLower(value)
	case "title", "titlecase":
		return strings.Title(strings.ToLower(value))
	case "capitalize":
		if len(value) == 0 {
			return value
		}
		return strings.ToUpper(string(value[0])) + strings.ToLower(value[1:])
	default:
		return value
	}
}

// addCommasToNumber adds comma separators to numbers for currency formatting
func (ep *ExpressionProcessor) addCommasToNumber(val float64) string {
	str := fmt.Sprintf("%.2f", val)
	parts := strings.Split(str, ".")

	// Add commas to integer part
	intPart := parts[0]
	if len(intPart) > 3 {
		var result []string
		for i, digit := range intPart {
			if i > 0 && (len(intPart)-i)%3 == 0 {
				result = append(result, ",")
			}
			result = append(result, string(digit))
		}
		intPart = strings.Join(result, "")
	}

	return intPart + "." + parts[1]
}

// ============================================================================
// TRANSFORMATION EXPRESSIONS {{TRANSFORM:...}}
// ============================================================================

// evaluateTransformationExpression handles comprehensive data transformation
func (ep *ExpressionProcessor) evaluateTransformationExpression(expression string, masterDTO *common_dto.MasterDTO, serviceNameMap map[string]*models.EsaLog) string {
	// The expression parameter is the inner content after "TRANSFORM:"
	// Find the FIRST colon at depth 0 (not the last) to separate field path from transformation
	// This is critical for operations like split:delimiter:index where we need the full transformation string
	separatorColonIndex := -1
	depth := 0

findFirstColon:
	for i := 0; i < len(expression); i++ {
		char := expression[i]
		switch char {
		case '{':
			if i < len(expression)-1 && expression[i+1] == '{' {
				depth++
				i++
			}
		case '}':
			if i < len(expression)-1 && expression[i+1] == '}' {
				depth--
				i++
			}
		case ':':
			if depth == 0 {
				// Found first colon at depth 0 - this separates field path from transformation
				separatorColonIndex = i
				break findFirstColon // Break out of for loop, not just switch
			}
		}
	}

	if separatorColonIndex == -1 {
		return ""
	}

	fieldPath := strings.TrimSpace(expression[:separatorColonIndex])
	transformation := strings.TrimSpace(expression[separatorColonIndex+1:])

	// Enhanced parameter processing - supports literals, fallbacks, and nested expressions
	// Always use unified parameter processing for consistent handling
	// This fixes the nested expression literal value bug
	fieldValue := ep.processUnifiedParameter(fieldPath, masterDTO, serviceNameMap)

	if fieldValue == "" {
		return ""
	}

	switch transformation {
	case "uppercase", "upper":
		return strings.ToUpper(fieldValue)
	case "lowercase", "lower":
		return strings.ToLower(fieldValue)
	case "trim":
		return strings.TrimSpace(fieldValue)
	case "reverse":
		return ep.reverseString(fieldValue)
	case "length":
		return fmt.Sprintf("%d", len(fieldValue))
	case "remove_spaces":
		return strings.ReplaceAll(fieldValue, " ", "")
	case "normalize_spaces":
		return regexp.MustCompile(`\\s+`).ReplaceAllString(strings.TrimSpace(fieldValue), " ")
	default:
		if strings.HasPrefix(transformation, "truncate:") {
			return ep.handleTruncateTransformation(fieldValue, transformation)
		} else if strings.HasPrefix(transformation, "substring:") {
			return ep.handleSubstringTransformation(fieldValue, transformation)
		} else if strings.HasPrefix(transformation, "pad_left:") {
			return ep.handlePadLeftTransformation(fieldValue, transformation)
		} else if strings.HasPrefix(transformation, "pad_right:") {
			return ep.handlePadRightTransformation(fieldValue, transformation)
		} else if strings.HasPrefix(transformation, "format_time:") {
			return ep.handleFormatTimeTransformation(fieldValue, transformation)
		} else if strings.HasPrefix(transformation, "parse_time:") {
			return ep.handleParseTimeTransformation(fieldValue, transformation)
		} else if strings.HasPrefix(transformation, "split:") {
			return ep.handleSplitTransformation(fieldValue, transformation)
		} else if strings.HasPrefix(transformation, "chunk:") {
			return ep.handleChunkTransformation(fieldValue, transformation)
		} else if strings.HasPrefix(transformation, "replace:") {
			return ep.handleReplaceTransformation(fieldValue, transformation, masterDTO, serviceNameMap)
		}
		return fieldValue
	}
}

// handleTruncateTransformation truncates string to specified length
func (ep *ExpressionProcessor) handleTruncateTransformation(value, transformation string) string {
	parts := strings.Split(transformation, ":")
	if len(parts) < 2 {
		return value
	}

	length, err := strconv.Atoi(parts[1])
	if err != nil || length < 0 {
		return value
	}

	if len(value) <= length {
		return value
	}

	if length <= 3 {
		return value[:length]
	}

	return value[:length-3] + "..."
}

// handleSubstringTransformation extracts substring from start index to end or specific length
func (ep *ExpressionProcessor) handleSubstringTransformation(value, transformation string) string {
	parts := strings.Split(transformation, ":")
	if len(parts) < 2 {
		return value
	}

	startIndex, err := strconv.Atoi(parts[1])
	if err != nil || startIndex < 0 {
		return value
	}

	// If start index is beyond string length, return empty string
	if startIndex >= len(value) {
		return ""
	}

	// If only start index provided, take from start to end
	if len(parts) == 2 {
		return value[startIndex:]
	}

	// If length is also provided, take specific length from start
	if len(parts) >= 3 {
		length, err := strconv.Atoi(parts[2])
		if err != nil || length < 0 {
			return value
		}

		endIndex := startIndex + length
		if endIndex > len(value) {
			endIndex = len(value)
		}
		return value[startIndex:endIndex]
	}

	return value
}

// handlePadLeftTransformation pads string on the left
func (ep *ExpressionProcessor) handlePadLeftTransformation(value, transformation string) string {
	parts := strings.Split(transformation, ":")
	if len(parts) < 3 {
		return value
	}

	totalLength, err := strconv.Atoi(parts[1])
	if err != nil || totalLength <= len(value) {
		return value
	}

	padChar := parts[2]
	if len(padChar) == 0 {
		padChar = " "
	} else {
		padChar = string(padChar[0]) // Use first character only
	}

	padding := strings.Repeat(padChar, totalLength-len(value))
	return padding + value
}

// handlePadRightTransformation pads string on the right
func (ep *ExpressionProcessor) handlePadRightTransformation(value, transformation string) string {
	parts := strings.Split(transformation, ":")
	if len(parts) < 3 {
		return value
	}

	totalLength, err := strconv.Atoi(parts[1])
	if err != nil || totalLength <= len(value) {
		return value
	}

	padChar := parts[2]
	if len(padChar) == 0 {
		padChar = " "
	} else {
		padChar = string(padChar[0]) // Use first character only
	}

	padding := strings.Repeat(padChar, totalLength-len(value))
	return value + padding
}

// handleFormatTimeTransformation formats time with Go time format
func (ep *ExpressionProcessor) handleFormatTimeTransformation(value, transformation string) string {
	targetFormat := strings.TrimSpace(strings.TrimPrefix(transformation, "format_time:"))
	if targetFormat == "" {
		return value
	}

	parsedTime, ok := parseStandardDateTime(value)
	if !ok {
		return value
	}

	// Use translateDateFormat so pattern names (YYYY-MM-DD, DDMMYYYY, etc.) work per §10.2
	layout := ep.translateDateFormat(targetFormat)
	return parsedTime.Format(layout)
}

// handleParseTimeTransformation converts time from one format to another
func (ep *ExpressionProcessor) handleParseTimeTransformation(value, transformation string) string {
	sourceFormat, targetFormat, ok := ep.parseTimeFormatsFromTransformation(value, transformation)
	if !ok {
		return value
	}

	// Handle special formats
	var parsedTime time.Time
	var err error

	if sourceFormat == "Unix" {
		timestamp, parseErr := strconv.ParseInt(value, 10, 64)
		if parseErr != nil {
			return value
		}
		parsedTime = time.Unix(timestamp, 0)
	} else {
		sourceLayout := ep.translateDateFormat(sourceFormat)
		parsedTime, err = time.Parse(sourceLayout, value)
		if err != nil {
			return value
		}
	}

	// Format output
	if targetFormat == "Unix" {
		return fmt.Sprintf("%d", parsedTime.Unix())
	}
	targetLayout := ep.translateDateFormat(targetFormat)
	return parsedTime.Format(targetLayout)
}

func (ep *ExpressionProcessor) parseTimeFormatsFromTransformation(value, transformation string) (string, string, bool) {
	payload := strings.TrimSpace(strings.TrimPrefix(transformation, "parse_time:"))
	if payload == "" {
		return "", "", false
	}
	trimmedValue := strings.TrimSpace(value)

	if idx := strings.Index(payload, formatDatePatternSeparator); idx >= 0 {
		sourceFormat := strings.TrimSpace(payload[:idx])
		targetFormat := strings.TrimSpace(payload[idx+len(formatDatePatternSeparator):])
		if sourceFormat != "" && targetFormat != "" {
			return sourceFormat, targetFormat, true
		}
	}

	// Fast path: common non-ambiguous form parse_time:sourceFormat:targetFormat
	if strings.Count(payload, ":") == 1 {
		parts := strings.SplitN(payload, ":", 2)
		sourceFormat := strings.TrimSpace(parts[0])
		targetFormat := strings.TrimSpace(parts[1])
		if sourceFormat != "" && targetFormat != "" {
			if ep.canParseTimeValueWithFormatTrimmed(trimmedValue, sourceFormat) {
				return sourceFormat, targetFormat, true
			}
		}
	}

	parseableCache := make(map[string]bool, 8)
	canParseCached := func(sourceFormat string) bool {
		if result, exists := parseableCache[sourceFormat]; exists {
			return result
		}
		result := ep.canParseTimeValueWithFormatTrimmed(trimmedValue, sourceFormat)
		parseableCache[sourceFormat] = result
		return result
	}

	bestSource := ""
	bestTarget := ""
	for i := strings.IndexByte(payload, ':'); i >= 0; {
		sourceFormat := strings.TrimSpace(payload[:i])
		targetFormat := strings.TrimSpace(payload[i+1:])
		if sourceFormat == "" || targetFormat == "" {
			next := strings.IndexByte(payload[i+1:], ':')
			if next < 0 {
				break
			}
			i = i + 1 + next
			continue
		}
		if !canParseCached(sourceFormat) {
			next := strings.IndexByte(payload[i+1:], ':')
			if next < 0 {
				break
			}
			i = i + 1 + next
			continue
		}
		if len(sourceFormat) > len(bestSource) {
			bestSource = sourceFormat
			bestTarget = targetFormat
		}
		next := strings.IndexByte(payload[i+1:], ':')
		if next < 0 {
			break
		}
		i = i + 1 + next
	}

	if bestSource != "" && bestTarget != "" {
		return bestSource, bestTarget, true
	}

	parts := strings.SplitN(payload, ":", 2)
	if len(parts) == 2 && strings.TrimSpace(parts[0]) != "" && strings.TrimSpace(parts[1]) != "" {
		return strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1]), true
	}

	return "", "", false
}

func (ep *ExpressionProcessor) canParseTimeValueWithFormat(value, format string) bool {
	return ep.canParseTimeValueWithFormatTrimmed(strings.TrimSpace(value), format)
}

func (ep *ExpressionProcessor) canParseTimeValueWithFormatTrimmed(value, format string) bool {
	sourceFormat := strings.TrimSpace(format)
	if strings.EqualFold(sourceFormat, "Unix") {
		_, err := strconv.ParseInt(value, 10, 64)
		return err == nil
	}
	layout := ep.translateDateFormat(sourceFormat)
	_, err := time.Parse(layout, value)
	return err == nil
}

// handleSplitTransformation splits a string by delimiter and returns JSON array or specific element
// Syntax: split:delimiter or split:delimiter:index
// Supports both simple string delimiters and regex patterns
func (ep *ExpressionProcessor) handleSplitTransformation(value, transformation string) string {
	parts := strings.Split(transformation, ":")
	if len(parts) < 2 {
		return value
	}

	// Don't trim the delimiter - spaces and other whitespace are valid delimiters
	delimiter := parts[1]
	if delimiter == "" {
		return value
	}

	// Check if delimiter is a regex pattern (starts and ends with /)
	var splitResult []string
	if len(delimiter) >= 2 && delimiter[0] == '/' && delimiter[len(delimiter)-1] == '/' {
		// Regex pattern - remove leading and trailing /
		pattern := delimiter[1 : len(delimiter)-1]
		regex, err := regexp.Compile(pattern)
		if err != nil {
			// Invalid regex, fall back to literal string
			splitResult = strings.Split(value, delimiter)
		} else {
			splitResult = regex.Split(value, -1)
		}
	} else {
		// Simple string delimiter
		splitResult = strings.Split(value, delimiter)
	}

	// If index is specified, return specific element
	if len(parts) >= 3 {
		indexStr := strings.TrimSpace(parts[2])
		index, err := strconv.Atoi(indexStr)
		if err == nil && index >= 0 && index < len(splitResult) {
			return splitResult[index]
		} else if err == nil && index < 0 {
			// Negative index: count from end
			actualIndex := len(splitResult) + index
			if actualIndex >= 0 && actualIndex < len(splitResult) {
				return splitResult[actualIndex]
			}
		}
		// Invalid index, return empty string
		return ""
	}

	// Return JSON array string
	resultJSON, err := json.Marshal(splitResult)
	if err != nil {
		return "[]"
	}
	return string(resultJSON)
}

// handleChunkTransformation splits a string into lines using comma or whitespace as wrap boundaries
// without breaking words. Commas stay attached to the preceding token and whitespace is normalized.
// Syntax: chunk:maxLength or chunk:maxLength:index
// - chunk:40 returns a JSON array of lines, each packed as close as possible under 40 chars.
// - chunk:40:0 returns the first line, chunk:40:1 the second, etc.; empty string if index out of range.
func (ep *ExpressionProcessor) handleChunkTransformation(value, transformation string) string {
	parts := strings.Split(transformation, ":")
	if len(parts) < 2 {
		return value
	}

	maxLength, err := strconv.Atoi(strings.TrimSpace(parts[1]))
	if err != nil || maxLength <= 0 {
		return value
	}

	value = strings.TrimSpace(value)
	if value == "" {
		if len(parts) >= 3 {
			return ""
		}
		return "[]"
	}

	type chunkToken struct {
		text        string
		prefixSpace bool
	}

	tokenizeChunkValue := func(input string) []chunkToken {
		var tokens []chunkToken
		var current strings.Builder
		hasCurrent := false
		prefixSpace := false
		pendingSpace := false

		flushCurrent := func() {
			if !hasCurrent {
				return
			}
			tokens = append(tokens, chunkToken{
				text:        current.String(),
				prefixSpace: prefixSpace,
			})
			current.Reset()
			hasCurrent = false
			prefixSpace = false
		}

		for _, r := range input {
			switch {
			case unicode.IsSpace(r):
				flushCurrent()
				pendingSpace = true
			case r == ',':
				if hasCurrent {
					current.WriteRune(r)
				} else if len(tokens) > 0 {
					tokens[len(tokens)-1].text += string(r)
				} else {
					tokens = append(tokens, chunkToken{text: string(r)})
				}
				flushCurrent()
			default:
				if !hasCurrent {
					hasCurrent = true
					prefixSpace = pendingSpace
					pendingSpace = false
				}
				current.WriteRune(r)
			}
		}
		flushCurrent()
		return tokens
	}

	tokens := tokenizeChunkValue(value)
	var lines []string
	var current strings.Builder
	currentLen := 0

	appendCurrentLine := func() {
		if current.Len() == 0 {
			return
		}
		lines = append(lines, current.String())
		current.Reset()
		currentLen = 0
	}

	for _, token := range tokens {
		need := len(token.text)
		if currentLen > 0 && token.prefixSpace {
			need++
		}

		if currentLen > 0 && currentLen+need < maxLength {
			if token.prefixSpace {
				current.WriteByte(' ')
				currentLen++
			}
			current.WriteString(token.text)
			currentLen += len(token.text)
			continue
		}

		appendCurrentLine()

		if len(token.text) >= maxLength {
			// Cannot satisfy the limit without breaking the token, so preserve it as-is.
			lines = append(lines, token.text)
			continue
		}

		current.WriteString(token.text)
		currentLen = len(token.text)
	}
	appendCurrentLine()

	if len(parts) >= 3 {
		index, err := strconv.Atoi(strings.TrimSpace(parts[2]))
		if err != nil {
			return ""
		}
		if index < 0 {
			index = len(lines) + index
		}
		if index >= 0 && index < len(lines) {
			return lines[index]
		}
		return ""
	}

	resultJSON, err := json.Marshal(lines)
	if err != nil {
		return "[]"
	}
	return string(resultJSON)
}

// handleReplaceTransformation replaces all occurrences of a substring with a new value
// Syntax: replace:searchValue:replacementValue
// Both search and replacement parameters support literals, placeholders, expressions, and fallbacks
func (ep *ExpressionProcessor) handleReplaceTransformation(value, transformation string, masterDTO *common_dto.MasterDTO, serviceNameMap map[string]*models.EsaLog) string {
	parts := strings.SplitN(transformation, ":", 3)
	if len(parts) < 3 {
		return value
	}

	searchParam := parts[1]
	replacementParam := parts[2]

	searchValue := ep.processUnifiedParameter(searchParam, masterDTO, serviceNameMap)
	replacementValue := ep.processUnifiedParameter(replacementParam, masterDTO, serviceNameMap)

	// Avoid infinite loops when search value is empty
	if searchValue == "" {
		return value
	}

	return strings.ReplaceAll(value, searchValue, replacementValue)
}

// ============================================================================
// CONCATENATION EXPRESSIONS {{CONCAT:...}}
// ============================================================================

// evaluateConcatenationExpression handles string concatenation with unified parameter processing
func (ep *ExpressionProcessor) evaluateConcatenationExpression(expression string, masterDTO *common_dto.MasterDTO, serviceNameMap map[string]*models.EsaLog) string {
	parts := strings.Split(expression, ":")
	if len(parts) < 2 {
		return ""
	}

	var result []string

	for _, part := range parts {
		// Handle literal spaces and empty parts
		if part == "" {
			result = append(result, "")
			continue
		}

		// Handle literal spaces (single space should remain as space)
		if part == " " {
			result = append(result, " ")
			continue
		}

		// Use unified parameter processing for consistent fallback and nested expression support
		// This enables CONCAT to work with fallback chains and nested expressions
		fieldValue := ep.processUnifiedParameter(part, masterDTO, serviceNameMap)

		// Add all resolved values
		result = append(result, fieldValue)
	}

	// Join with empty separator for exact concatenation
	return strings.Join(result, "")
}

// ============================================================================
// CONDITIONAL EXPRESSIONS {{CONDITION:...}} - ENHANCED WITH MULTI-CONDITIONS
// ============================================================================

// findFirstTwoColonsAtDepth0 returns the indices of the first two colons that occur
// isInternalMarkerColon checks whether the colon at position colonPos in expression
// is part of an internal type marker prefix like __NUMERIC_INT__: or __ARRAY_PARSE__:.
// These markers are injected during expression evaluation and must not be treated as
// CONDITION delimiter colons.
func isInternalMarkerColon(expression string, colonPos int) bool {
	markers := []string{
		"__NUMERIC_INT__",
		"__ARRAY_PARSE__",
		"__JSON_PARSE__",
		"__NULL_NUMERIC__",
		"__BOOLEAN_TRUE__",
		"__BOOLEAN_FALSE__",
	}
	for _, marker := range markers {
		mLen := len(marker)
		if colonPos >= mLen && expression[colonPos-mLen:colonPos] == marker {
			return true
		}
	}
	return false
}

// stripInternalTypeMarker removes internal type-marker prefixes that are injected
// during nested expression evaluation, so that comparison operands in CONDITION
// expressions see the clean underlying value.
func stripInternalTypeMarker(value string) string {
	prefixes := []string{
		"__NUMERIC_INT__:",
		"__ARRAY_PARSE__:",
		"__JSON_PARSE__:",
	}
	for _, prefix := range prefixes {
		if strings.HasPrefix(value, prefix) {
			return value[len(prefix):]
		}
	}
	switch value {
	case "__NULL_NUMERIC__":
		return ""
	case "__BOOLEAN_TRUE__":
		return "true"
	case "__BOOLEAN_FALSE__":
		return "false"
	}
	return value
}

// at "depth 0" (outside {{ }}, outside [...], outside (...), and outside quoted strings).
// Used so that CONDITION: cond : trueValue : falseValue parses correctly when
// trueValue or falseValue contain nested expressions with colons (e.g. {{FORMAT:text:x:lowercase}})
// or when the condition contains IN ('a','b') with no colon inside.
// Returns (-1, -1) if fewer than two such colons exist.
func (ep *ExpressionProcessor) findFirstTwoColonsAtDepth0(expression string) (first, second int) {
	first, second = -1, -1
	braceDepth := 0
	bracketDepth := 0
	parenDepth := 0
	inQuotes := false
	var quoteChar byte

	for i := 0; i < len(expression); i++ {
		char := expression[i]
		switch char {
		case '"', '\'':
			if !inQuotes {
				inQuotes = true
				quoteChar = char
			} else if char == quoteChar {
				inQuotes = false
			}
		case '(':
			if !inQuotes {
				parenDepth++
			}
		case ')':
			if !inQuotes {
				parenDepth--
			}
		case '{':
			if !inQuotes {
				if i < len(expression)-1 && expression[i+1] == '{' {
					braceDepth++
					i++
				} else {
					bracketDepth++
				}
			}
		case '}':
			if !inQuotes {
				if i < len(expression)-1 && expression[i+1] == '}' {
					braceDepth--
					i++
				} else {
					bracketDepth--
				}
			}
		case '[':
			if !inQuotes {
				bracketDepth++
			}
		case ']':
			if !inQuotes {
				bracketDepth--
			}
		case '\\':
			if inQuotes && i+1 < len(expression) {
				i++ // skip escaped char (\' / \\) inside quotes so it can't toggle quote state
			}
		case ':':
			if !inQuotes && braceDepth == 0 && bracketDepth == 0 && parenDepth == 0 {
				if isInternalMarkerColon(expression, i) {
					continue
				}
				if first == -1 {
					first = i
				} else {
					second = i
					return first, second
				}
			}
		}
	}
	return first, second
}

// findLastColonAtDepth0 returns the index of the last colon at depth 0 (same depth rules as findFirstTwoColonsAtDepth0).
// Used to detect a trailing :'literal' fallback in a CONDITION branch (e.g. {{CONDITION:...}}:'null').
func (ep *ExpressionProcessor) findLastColonAtDepth0(expression string) int {
	last := -1
	braceDepth := 0
	bracketDepth := 0
	parenDepth := 0
	inQuotes := false
	var quoteChar byte

	for i := 0; i < len(expression); i++ {
		char := expression[i]
		switch char {
		case '"', '\'':
			if !inQuotes {
				inQuotes = true
				quoteChar = char
			} else if char == quoteChar {
				inQuotes = false
			}
		case '(':
			if !inQuotes {
				parenDepth++
			}
		case ')':
			if !inQuotes {
				parenDepth--
			}
		case '{':
			if !inQuotes {
				if i < len(expression)-1 && expression[i+1] == '{' {
					braceDepth++
					i++
				} else {
					bracketDepth++
				}
			}
		case '}':
			if !inQuotes {
				if i < len(expression)-1 && expression[i+1] == '}' {
					braceDepth--
					i++
				} else {
					bracketDepth--
				}
			}
		case '[':
			if !inQuotes {
				bracketDepth++
			}
		case ']':
			if !inQuotes {
				bracketDepth--
			}
		case '\\':
			if inQuotes && i+1 < len(expression) {
				i++ // skip escaped char (\' / \\) inside quotes so it can't toggle quote state
			}
		case ':':
			if !inQuotes && braceDepth == 0 && bracketDepth == 0 && parenDepth == 0 {
				if isInternalMarkerColon(expression, i) {
					continue
				}
				last = i
			}
		}
	}
	return last
}

// isQuotedLiteral returns true if s is a non-empty string wrapped in single or double quotes (and nothing else).
func isQuotedLiteral(s string) bool {
	s = strings.TrimSpace(s)
	if len(s) < 2 {
		return false
	}
	return (s[0] == '\'' && s[len(s)-1] == '\'') || (s[0] == '"' && s[len(s)-1] == '"')
}

// evaluateConditionalExpression handles conditional logic with multi-condition support.
// Splitting uses depth-aware parsing so colons inside nested {{...}} or quotes are not
// treated as delimiters (condition : trueValue : falseValue).
// If the expression still has a leading "CONDITION:" (e.g. from recursive processing),
// it is stripped so the first colon at depth 0 is not the one in "CONDITION:".
func (ep *ExpressionProcessor) evaluateConditionalExpression(expression string, masterDTO *common_dto.MasterDTO, serviceNameMap map[string]*models.EsaLog) string {
	expression = strings.TrimSpace(expression)
	if strings.HasPrefix(expression, "CONDITION:") {
		expression = strings.TrimSpace(expression[10:])
	}

	firstColon, secondColon := ep.findFirstTwoColonsAtDepth0(expression)
	if debugPipeline {
		fmt.Printf("[CONDITION_EVAL] firstColon=%d secondColon=%d exprLen=%d\n  expr: %s\n",
			firstColon, secondColon, len(expression), debugTruncate(expression, 400))
	}
	if firstColon == -1 || secondColon == -1 {
		if debugPipeline {
			fmt.Printf("[CONDITION_EVAL] FALLBACK to legacy ternary (colons not found)\n")
		}
		return ep.evaluateLegacyTernaryExpression(expression, masterDTO, serviceNameMap)
	}

	condition := strings.TrimSpace(expression[:firstColon])
	trueValue := strings.TrimSpace(expression[firstColon+1 : secondColon])
	falseValue := strings.TrimSpace(expression[secondColon+1:])

	// Resolve ((Service.path)) in the condition before evaluation. Use raw getServiceResponseValue
	// (no "false" for empty) so that when a service is not in the sequence, the placeholder becomes
	// "" and the gate evaluates correctly (e.g. ((MobileUANService)) != '' → '' != '' → false → else branch null).
	condition = ep.doubleParenthesesRegex.ReplaceAllStringFunc(condition, func(match string) string {
		placeholder := match[2 : len(match)-2]
		return ep.getServiceResponseValue(placeholder, serviceNameMap, nil)
	})

	if debugPipeline {
		fmt.Printf("[CONDITION_EVAL] condition: %s\n  trueValue: %s\n  falseValue: %s\n",
			debugTruncate(condition, 200), debugTruncate(trueValue, 200), debugTruncate(falseValue, 200))
	}

	// Evaluate the condition using the enhanced multi-condition parser. When the condition cannot
	// be evaluated at all (malformed / no locatable comparison operator — e.g. an operator buried
	// inside a large resolved service body or unbalanced placeholders), surface an error and
	// resolve the whole CONDITION expression to blank instead of silently taking the false branch
	// (BUG-2). This is scoped to the CONDITION expression only; the surrounding expression continues
	// to process the (now blank) result through its normal fallbacks.
	isTrue, evaluable := ep.evaluateEnhancedConditionChecked(condition, masterDTO, serviceNameMap)
	if !evaluable {
		ep.logError("CONDITION expression could not be evaluated; resolving to blank",
			"condition", debugTruncate(condition, 500),
			"expression", debugTruncate(expression, 500))
		return ""
	}

	chosen := falseValue
	if isTrue {
		chosen = trueValue
	}
	if debugPipeline {
		fmt.Printf("[CONDITION_EVAL] isTrue=%v chosen: %s\n", isTrue, debugTruncate(chosen, 200))
	}
	result := ep.processConditionalBranchWithFallback(chosen, masterDTO, serviceNameMap)
	if debugPipeline {
		fmt.Printf("[CONDITION_EVAL] FINAL result: %s\n", debugTruncate(result, 200))
	}
	return result
}

// processConditionalBranchWithFallback processes a CONDITION branch. If the branch has a trailing
// :'literal' at depth 0 (e.g. {{CONDITION:...}}:'null'), evaluates the main part and returns the
// quoted literal only when the result is empty; otherwise returns the evaluated result.
func (ep *ExpressionProcessor) processConditionalBranchWithFallback(branch string, masterDTO *common_dto.MasterDTO, serviceNameMap map[string]*models.EsaLog) string {
	lastColon := ep.findLastColonAtDepth0(branch)
	if lastColon >= 0 {
		after := strings.TrimSpace(branch[lastColon+1:])
		if isQuotedLiteral(after) {
			mainPart := strings.TrimSpace(branch[:lastColon])
			result := ep.processUnifiedParameter(mainPart, masterDTO, serviceNameMap)
			if strings.TrimSpace(result) == "" {
				return strings.Trim(strings.TrimSpace(after), "'\"")
			}
			return result
		}
	}
	return ep.processUnifiedParameter(branch, masterDTO, serviceNameMap)
}

// evaluateLegacyTernaryExpression handles legacy ? : syntax for backward compatibility
func (ep *ExpressionProcessor) evaluateLegacyTernaryExpression(expression string, masterDTO *common_dto.MasterDTO, serviceNameMap map[string]*models.EsaLog) string {
	if strings.Contains(expression, "?") && strings.Contains(expression, ":") {
		parts := strings.Split(expression, "?")
		if len(parts) == 2 {
			condition := strings.TrimSpace(parts[0])
			valueParts := strings.Split(parts[1], ":")
			if len(valueParts) == 2 {
				trueValue := strings.Trim(strings.TrimSpace(valueParts[0]), "'\"")
				falseValue := strings.Trim(strings.TrimSpace(valueParts[1]), "'\"")

				// Evaluate using enhanced condition parser
				if ep.evaluateEnhancedCondition(condition, masterDTO, serviceNameMap) {
					return trueValue
				}
				return falseValue
			}
		}
	}
	return ""
}

// evaluateEnhancedCondition handles multi-condition expressions with disambiguation.
// It returns only the boolean result (backward compatible for all existing callers). Use
// evaluateEnhancedConditionChecked when the caller must distinguish an unevaluable condition.
func (ep *ExpressionProcessor) evaluateEnhancedCondition(condition string, masterDTO *common_dto.MasterDTO, serviceNameMap map[string]*models.EsaLog) bool {
	result, _ := ep.evaluateEnhancedConditionChecked(condition, masterDTO, serviceNameMap)
	return result
}

// evaluateEnhancedConditionChecked mirrors evaluateEnhancedCondition's phase routing but also
// reports whether the condition could actually be evaluated. evaluable=false propagates up so the
// CONDITION expression resolves to blank instead of silently taking the false branch (BUG-2). The
// boolean result is identical to evaluateEnhancedCondition for every evaluable case.
func (ep *ExpressionProcessor) evaluateEnhancedConditionChecked(condition string, masterDTO *common_dto.MasterDTO, serviceNameMap map[string]*models.EsaLog) (bool, bool) {
	condition = strings.TrimSpace(condition)

	// Phase 0: IN operator with parentheses list — handle before parenthesis check so "field IN ('a','b')" is not sent to logical parser
	if strings.Contains(condition, " IN ") {
		parts := strings.SplitN(condition, " IN ", 2)
		if len(parts) == 2 {
			listPart := strings.TrimSpace(parts[1])
			if strings.HasPrefix(listPart, "(") && strings.HasSuffix(listPart, ")") {
				return ep.evaluateSimpleConditionChecked(condition, masterDTO, serviceNameMap)
			}
		}
	}

	// Phase 1: Check for explicit keywords (AND, OR) before generic parentheses handling.
	// Important: IN lists use parentheses, e.g. "x IN ('a','b') AND y != ''".
	// If parentheses are handled first, parser can misroute and mis-evaluate these conditions.
	if strings.Contains(condition, " AND ") || strings.Contains(condition, " OR ") {
		return ep.evaluateKeywordExpressionChecked(condition, masterDTO, serviceNameMap)
	}

	// Phase 2: Check for parentheses - if found, use logical parser.
	if strings.Contains(condition, "(") && strings.Contains(condition, ")") {
		return ep.evaluateLogicalExpressionChecked(condition, masterDTO, serviceNameMap)
	}

	// Phase 3: Check for logical operators at top level (context-aware)
	if ep.containsTopLevelLogicalOperators(condition) {
		// Within CONDITION context, treat || and && as logical operators
		return ep.evaluateLogicalExpressionChecked(condition, masterDTO, serviceNameMap)
	}

	// Phase 4: Simple comparison (backward compatible)
	return ep.evaluateSimpleConditionChecked(condition, masterDTO, serviceNameMap)
}

// evaluateLogicalExpression handles complex logical expressions with &&, ||, and parentheses.
func (ep *ExpressionProcessor) evaluateLogicalExpression(expression string, masterDTO *common_dto.MasterDTO, serviceNameMap map[string]*models.EsaLog) bool {
	result, _ := ep.evaluateLogicalExpressionChecked(expression, masterDTO, serviceNameMap)
	return result
}

// evaluateLogicalExpressionChecked is evaluateLogicalExpression with an evaluable flag. When the
// expression cannot be parsed into an AST it falls back to the checked simple-condition evaluator,
// so a genuinely unparseable comparison is reported as unevaluable rather than defaulting to false.
func (ep *ExpressionProcessor) evaluateLogicalExpressionChecked(expression string, masterDTO *common_dto.MasterDTO, serviceNameMap map[string]*models.EsaLog) (bool, bool) {
	// Parse the expression into an AST (Abstract Syntax Tree)
	ast, err := ep.parseLogicalExpressionAST(expression)
	if err != nil {
		// Fallback to simple condition evaluation
		return ep.evaluateSimpleConditionChecked(expression, masterDTO, serviceNameMap)
	}

	return ep.evaluateLogicalASTChecked(ast, masterDTO, serviceNameMap)
}

// evaluateKeywordExpression handles expressions with AND/OR keywords.
func (ep *ExpressionProcessor) evaluateKeywordExpression(expression string, masterDTO *common_dto.MasterDTO, serviceNameMap map[string]*models.EsaLog) bool {
	result, _ := ep.evaluateKeywordExpressionChecked(expression, masterDTO, serviceNameMap)
	return result
}

// evaluateKeywordExpressionChecked is evaluateKeywordExpression with an evaluable flag. A definite
// short-circuit (a false operand in AND, a true operand in OR) is always evaluable. Otherwise, if
// any operand is unevaluable, the whole keyword expression is reported unevaluable. The boolean
// result matches evaluateKeywordExpression for every evaluable case.
func (ep *ExpressionProcessor) evaluateKeywordExpressionChecked(expression string, masterDTO *common_dto.MasterDTO, serviceNameMap map[string]*models.EsaLog) (bool, bool) {
	// Prefer direct keyword splitting at top level (outside quotes/nested expressions).
	// This avoids parse fallbacks that can mis-evaluate conditions mixing IN + AND/OR.
	andParts := ep.splitTopLevelByKeyword(expression, "AND")
	if len(andParts) > 1 {
		anyUnevaluable := false
		for _, part := range andParts {
			r, ok := ep.evaluateEnhancedConditionChecked(part, masterDTO, serviceNameMap)
			if !ok {
				anyUnevaluable = true
				continue
			}
			if !r {
				return false, true // definite false short-circuits the whole AND
			}
		}
		if anyUnevaluable {
			return false, false
		}
		return true, true
	}

	orParts := ep.splitTopLevelByKeyword(expression, "OR")
	if len(orParts) > 1 {
		anyUnevaluable := false
		for _, part := range orParts {
			r, ok := ep.evaluateEnhancedConditionChecked(part, masterDTO, serviceNameMap)
			if !ok {
				anyUnevaluable = true
				continue
			}
			if r {
				return true, true // definite true short-circuits the whole OR
			}
		}
		if anyUnevaluable {
			return false, false
		}
		return false, true
	}

	// Fallback to existing symbol-based logical parser.
	expression = strings.ReplaceAll(expression, " AND ", " && ")
	expression = strings.ReplaceAll(expression, " OR ", " || ")
	return ep.evaluateLogicalExpressionChecked(expression, masterDTO, serviceNameMap)
}

// splitTopLevelByKeyword splits expression by keyword (AND/OR) only at top level:
// outside quotes, outside {{...}}, outside (...), outside [...].
func (ep *ExpressionProcessor) splitTopLevelByKeyword(expression, keyword string) []string {
	upperExpr := strings.ToUpper(expression)
	kw := " " + strings.ToUpper(strings.TrimSpace(keyword)) + " "
	if !strings.Contains(upperExpr, kw) {
		return []string{strings.TrimSpace(expression)}
	}

	parts := make([]string, 0, 4)
	start := 0
	inQuotes := false
	var quoteChar byte
	braceDepth := 0
	parenDepth := 0
	bracketDepth := 0

	for i := 0; i < len(expression); i++ {
		c := expression[i]
		if (c == '"' || c == '\'') && (i == 0 || expression[i-1] != '\\') {
			if !inQuotes {
				inQuotes = true
				quoteChar = c
			} else if c == quoteChar {
				inQuotes = false
				quoteChar = 0
			}
			continue
		}
		if inQuotes {
			continue
		}
		if i < len(expression)-1 && c == '{' && expression[i+1] == '{' {
			braceDepth++
			i++
			continue
		}
		if i < len(expression)-1 && c == '}' && expression[i+1] == '}' {
			if braceDepth > 0 {
				braceDepth--
			}
			i++
			continue
		}
		switch c {
		case '(':
			parenDepth++
		case ')':
			if parenDepth > 0 {
				parenDepth--
			}
		case '[':
			bracketDepth++
		case ']':
			if bracketDepth > 0 {
				bracketDepth--
			}
		}
		if braceDepth == 0 && parenDepth == 0 && bracketDepth == 0 {
			if i+len(kw) <= len(expression) && strings.ToUpper(expression[i:i+len(kw)]) == kw {
				part := strings.TrimSpace(expression[start:i])
				if part != "" {
					parts = append(parts, part)
				}
				start = i + len(kw)
				i = start - 1
			}
		}
	}

	last := strings.TrimSpace(expression[start:])
	if last != "" {
		parts = append(parts, last)
	}
	if len(parts) == 0 {
		return []string{strings.TrimSpace(expression)}
	}
	return parts
}

// containsTopLevelLogicalOperators checks for logical operators not in quotes or nested expressions
func (ep *ExpressionProcessor) containsTopLevelLogicalOperators(expression string) bool {
	var inQuotes bool
	var quoteChar rune
	var depth int
	var escapeNext bool

	for i, char := range expression {
		if escapeNext {
			escapeNext = false
			continue
		}
		switch char {
		case '\\':
			if inQuotes {
				escapeNext = true // skip next char (\' / \\) inside quotes
			}
		case '"', '\'':
			if !inQuotes {
				inQuotes = true
				quoteChar = char
			} else if char == quoteChar {
				inQuotes = false
			}
		case '{':
			if !inQuotes && i < len(expression)-1 && rune(expression[i+1]) == '{' {
				depth++
			}
		case '}':
			if !inQuotes && i < len(expression)-1 && rune(expression[i+1]) == '}' {
				depth--
			}
		case '&':
			if !inQuotes && depth == 0 && i < len(expression)-1 && rune(expression[i+1]) == '&' {
				return true
			}
		case '|':
			if !inQuotes && depth == 0 && i < len(expression)-1 && rune(expression[i+1]) == '|' {
				return true
			}
		}
	}

	return false
}

// LogicalAST represents the structure of a logical expression
type LogicalAST struct {
	Type       string // "comparison", "and", "or", "not"
	Left       *LogicalAST
	Right      *LogicalAST
	Operator   string // "==", "!=", ">", "<", ">=", "<="
	LeftValue  string // Field path or literal
	RightValue string // Field path or literal
}

// parseLogicalExpressionAST parses complex logical expressions into an AST
func (ep *ExpressionProcessor) parseLogicalExpressionAST(expression string) (*LogicalAST, error) {
	expression = strings.TrimSpace(expression)

	// Handle parentheses by finding the outermost logical operator
	if strings.HasPrefix(expression, "(") && strings.HasSuffix(expression, ")") {
		// Remove outer parentheses and parse inner expression
		inner := expression[1 : len(expression)-1]
		return ep.parseLogicalExpressionAST(inner)
	}

	// Find the main logical operator (|| has lower precedence than &&)
	orIndex := ep.findTopLevelOperator(expression, "||")
	if orIndex != -1 {
		left := strings.TrimSpace(expression[:orIndex])
		right := strings.TrimSpace(expression[orIndex+2:])

		leftAST, err := ep.parseLogicalExpressionAST(left)
		if err != nil {
			return nil, err
		}

		rightAST, err := ep.parseLogicalExpressionAST(right)
		if err != nil {
			return nil, err
		}

		return &LogicalAST{
			Type:  "or",
			Left:  leftAST,
			Right: rightAST,
		}, nil
	}

	// Find && operator
	andIndex := ep.findTopLevelOperator(expression, "&&")
	if andIndex != -1 {
		left := strings.TrimSpace(expression[:andIndex])
		right := strings.TrimSpace(expression[andIndex+2:])

		leftAST, err := ep.parseLogicalExpressionAST(left)
		if err != nil {
			return nil, err
		}

		rightAST, err := ep.parseLogicalExpressionAST(right)
		if err != nil {
			return nil, err
		}

		return &LogicalAST{
			Type:  "and",
			Left:  leftAST,
			Right: rightAST,
		}, nil
	}

	// Parse as simple comparison
	return ep.parseSimpleComparison(expression)
}

// findTopLevelOperator finds logical operators not within parentheses or quotes
func (ep *ExpressionProcessor) findTopLevelOperator(expression string, operator string) int {
	var depth int
	var inQuotes bool
	var quoteChar rune

	for i := 0; i <= len(expression)-len(operator); i++ {
		char := rune(expression[i])

		if inQuotes && char == '\\' && i+1 < len(expression) {
			i++ // skip escaped char (\' / \\) inside quotes so it can't toggle quote state
			continue
		}
		switch char {
		case '"', '\'':
			if !inQuotes {
				inQuotes = true
				quoteChar = char
			} else if char == quoteChar {
				inQuotes = false
			}
		case '(':
			if !inQuotes {
				depth++
			}
		case ')':
			if !inQuotes {
				depth--
			}
		}

		if !inQuotes && depth == 0 {
			if strings.HasPrefix(expression[i:], operator) {
				return i
			}
		}
	}

	return -1
}

// parseSimpleComparison parses a simple comparison into AST
func (ep *ExpressionProcessor) parseSimpleComparison(expression string) (*LogicalAST, error) {
	operators := []string{">=", "<=", "==", "!=", ">", "<"}

	for _, op := range operators {
		if strings.Contains(expression, op) {
			// Use the same operator position detection logic to avoid placeholder conflicts
			opIndex := ep.findOperatorPosition(expression, op)
			if opIndex == -1 {
				continue
			}

			// Split manually at the operator position
			leftPart := expression[:opIndex]
			rightPart := expression[opIndex+len(op):]

			if leftPart != "" && rightPart != "" {
				return &LogicalAST{
					Type:       "comparison",
					Operator:   op,
					LeftValue:  strings.TrimSpace(leftPart),
					RightValue: strings.TrimSpace(rightPart),
				}, nil
			}
		}
	}

	return nil, fmt.Errorf("invalid comparison expression: %s", expression)
}

// evaluateLogicalAST evaluates the logical AST.
func (ep *ExpressionProcessor) evaluateLogicalAST(ast *LogicalAST, masterDTO *common_dto.MasterDTO, serviceNameMap map[string]*models.EsaLog) bool {
	result, _ := ep.evaluateLogicalASTChecked(ast, masterDTO, serviceNameMap)
	return result
}

// evaluateLogicalASTChecked evaluates the logical AST and reports evaluability. A comparison leaf
// is always evaluable (its operator was already located during parsing). For and/or nodes a
// definite short-circuit result is evaluable; otherwise an unevaluable operand makes the node
// unevaluable. The boolean result matches evaluateLogicalAST for every evaluable case.
func (ep *ExpressionProcessor) evaluateLogicalASTChecked(ast *LogicalAST, masterDTO *common_dto.MasterDTO, serviceNameMap map[string]*models.EsaLog) (bool, bool) {
	switch ast.Type {
	case "and":
		leftResult, leftOK := ep.evaluateLogicalASTChecked(ast.Left, masterDTO, serviceNameMap)
		if leftOK && !leftResult {
			return false, true // definite false short-circuits AND
		}
		rightResult, rightOK := ep.evaluateLogicalASTChecked(ast.Right, masterDTO, serviceNameMap)
		if rightOK && !rightResult {
			return false, true // definite false on the right short-circuits AND
		}
		if !leftOK || !rightOK {
			return false, false
		}
		return leftResult && rightResult, true

	case "or":
		leftResult, leftOK := ep.evaluateLogicalASTChecked(ast.Left, masterDTO, serviceNameMap)
		if leftOK && leftResult {
			return true, true // definite true short-circuits OR
		}
		rightResult, rightOK := ep.evaluateLogicalASTChecked(ast.Right, masterDTO, serviceNameMap)
		if rightOK && rightResult {
			return true, true // definite true on the right short-circuits OR
		}
		if !leftOK || !rightOK {
			return false, false
		}
		return false, true

	case "comparison":
		return ep.evaluateComparison(ast.Operator, ast.LeftValue, ast.RightValue, masterDTO, serviceNameMap), true

	default:
		return false, false
	}
}

// evaluateComparison evaluates a single comparison with unified parameter processing
func (ep *ExpressionProcessor) evaluateComparison(operator, leftValue, rightValue string, masterDTO *common_dto.MasterDTO, serviceNameMap map[string]*models.EsaLog) bool {
	// Use unified parameter processing for consistent fallback and nested expression support
	leftResolved := ep.processUnifiedParameter(leftValue, masterDTO, serviceNameMap)
	rightResolved := ep.processUnifiedParameter(rightValue, masterDTO, serviceNameMap)

	// Remove quotes from right value if present
	rightResolved = strings.Trim(rightResolved, "\"'")

	// Try numeric comparison first
	leftNum, leftErr := strconv.ParseFloat(leftResolved, 64)
	rightNum, rightErr := strconv.ParseFloat(rightResolved, 64)

	if leftErr == nil && rightErr == nil {
		// Numeric comparison
		switch operator {
		case ">=":
			return leftNum >= rightNum
		case "<=":
			return leftNum <= rightNum
		case ">":
			return leftNum > rightNum
		case "<":
			return leftNum < rightNum
		case "==":
			return leftNum == rightNum
		case "!=":
			return leftNum != rightNum
		}
	}

	// Relational comparison with an empty/unresolved operand: treat as not-satisfied (false)
	// instead of falling into lexical string comparison. Without this, a missing value (e.g. an
	// out-of-range array index or absent field) makes "" < 30 evaluate true because "" sorts
	// before "30" lexically. Equality (==/!=) is intentionally left to the string branch below,
	// and non-empty operands still use lexical comparison (so ISO date-string ordering works).
	if leftResolved == "" || rightResolved == "" {
		switch operator {
		case ">", "<", ">=", "<=":
			return false
		}
	}

	// String comparison
	switch operator {
	case "==":
		return leftResolved == rightResolved
	case "!=":
		return leftResolved != rightResolved
	case ">":
		return leftResolved > rightResolved
	case "<":
		return leftResolved < rightResolved
	case ">=":
		return leftResolved >= rightResolved
	case "<=":
		return leftResolved <= rightResolved
	}

	return false
}

// findOperatorPosition finds the position of an operator, avoiding conflicts with placeholders.
//
// It ignores operator characters that appear inside:
//   - quoted string literals (single or double quotes) — critical when a ((Service)) placeholder
//     has been expanded into the condition and the resolved body is a JSON blob whose string
//     values contain stray/unbalanced '(' ')' '<' '>' or comparison symbols. Without quote
//     awareness those characters skew bracketDepth so the real top-level operator is never located
//     (see BUG-1: CRIF Bureau_Score -998).
//   - angle-bracket placeholders <...>
//   - nested ((...)) / {{...}} bracket groups
//
// bracketDepth is clamped at zero so an unbalanced ')' inside otherwise unquoted text cannot drive
// the depth negative and permanently suppress operator detection.
func (ep *ExpressionProcessor) findOperatorPosition(condition string, operator string) int {
	inAnglePlaceholder := false
	bracketDepth := 0
	inQuotes := false
	var quoteChar byte

	for i := 0; i <= len(condition)-len(operator); i++ {
		char := condition[i]

		// Inside a quoted literal: skip everything (including brackets and operator symbols) until
		// the matching closing quote. Handle backslash escaping so an escaped quote (\" or \') does
		// not prematurely end the region — important for marshaled JSON with escaped quotes.
		if inQuotes {
			if char == '\\' && i+1 < len(condition) {
				i++ // skip the escaped character
				continue
			}
			if char == quoteChar {
				inQuotes = false
				quoteChar = 0
			}
			continue
		}
		if char == '\'' || char == '"' {
			inQuotes = true
			quoteChar = char
			continue
		}

		// Track angle bracket placeholders <...>
		if char == '<' && !inAnglePlaceholder {
			// Check if this looks like a placeholder (contains letters/dots after <)
			if i+1 < len(condition) && (ep.isLetter(condition[i+1]) || condition[i+1] == '.') {
				inAnglePlaceholder = true
				continue
			}
		} else if char == '>' && inAnglePlaceholder {
			inAnglePlaceholder = false
			continue // Skip this character as it's the closing bracket
		}

		// Track nested brackets/expressions ((...)) and {{...}}
		switch char {
		case '{', '(':
			bracketDepth++
		case '}', ')':
			if bracketDepth > 0 {
				bracketDepth--
			}
		}

		// Check if we found the operator outside placeholders and brackets
		if !inAnglePlaceholder && bracketDepth == 0 {
			if i+len(operator) <= len(condition) && condition[i:i+len(operator)] == operator {
				// Make sure it's not part of a longer operator
				if operator == ">" {
					if i > 0 && condition[i-1] == '=' {
						continue // This is part of >=
					}
					if i+1 < len(condition) && condition[i+1] == '=' {
						continue // This is part of >=
					}
				}
				if operator == "<" {
					if i > 0 && condition[i-1] == '=' {
						continue // This is part of <=
					}
					if i+1 < len(condition) && condition[i+1] == '=' {
						continue // This is part of <=
					}
				}
				return i
			}
		}
	}

	return -1
}

// isLetter checks if a character is a letter
func (ep *ExpressionProcessor) isLetter(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

// isWithinISODateTime checks if a colon at position i is part of an ISO datetime
func (ep *ExpressionProcessor) isWithinISODateTime(text string, colonPos int) bool {
	// Look for ISO datetime pattern around the colon position
	// Patterns: YYYY-MM-DDTHH:MM:SS, YYYY-MM-DDTHH:MM:SSZ, etc.

	// Look backwards to find potential start of ISO datetime (YYYY-MM-DD pattern)
	start := -1
	for i := colonPos - 1; i >= 10; i-- { // Need at least 10 chars for YYYY-MM-DD
		if i >= 10 &&
			text[i-10:i-6] != "" && text[i-6] == '-' && text[i-3] == '-' && text[i] == 'T' &&
			ep.isDigit(text[i-10]) && ep.isDigit(text[i-9]) && ep.isDigit(text[i-8]) && ep.isDigit(text[i-7]) &&
			ep.isDigit(text[i-5]) && ep.isDigit(text[i-4]) &&
			ep.isDigit(text[i-2]) && ep.isDigit(text[i-1]) {
			start = i - 10
			break
		}
	}

	if start == -1 {
		return false
	}

	// Look forwards to find the end of the ISO datetime
	end := len(text)
	for i := start + 11; i < len(text); i++ { // Start after YYYY-MM-DDT
		if text[i] == ' ' || text[i] == '\t' {
			end = i
			break
		}
		// Stop at rule keywords
		if i+5 <= len(text) && text[i:i+5] == "RULES" {
			end = i
			break
		}
		// Stop at non-datetime characters (but allow : for time and Z for timezone)
		if !ep.isDigit(text[i]) && text[i] != ':' && text[i] != 'Z' && text[i] != '+' && text[i] != '-' {
			end = i
			break
		}
	}

	// Check if the colon is within this ISO datetime span
	if colonPos >= start && colonPos < end {
		return true
	}

	return false
}

// isDigit checks if a character is a digit
func (ep *ExpressionProcessor) isDigit(c byte) bool {
	return c >= '0' && c <= '9'
}

// normalizeEmptyForComparison returns "" if v is empty or JSON empty ("{}", "[]") so that
// ((Service)) != ” is false when the service body is {} or [].
func (ep *ExpressionProcessor) normalizeEmptyForComparison(v string) string {
	v = strings.TrimSpace(v)
	if v == "" || v == "{}" || v == "[]" {
		return ""
	}
	return v
}

// evaluateSimpleCondition evaluates simple boolean conditions (backward compatibility).
// Supports: IN (val1, val2, ...) with parentheses (case-sensitive), and comparisons with
// full expression resolution ({{ }} and placeholders) on both sides.
//
// It returns only the boolean result. Callers that must distinguish "evaluated to false" from
// "could not be evaluated at all" (BUG-2) should use evaluateSimpleConditionChecked.
func (ep *ExpressionProcessor) evaluateSimpleCondition(condition string, masterDTO *common_dto.MasterDTO, serviceNameMap map[string]*models.EsaLog) bool {
	result, _ := ep.evaluateSimpleConditionChecked(condition, masterDTO, serviceNameMap)
	return result
}

// evaluateSimpleConditionChecked is evaluateSimpleCondition with an extra evaluable flag.
// evaluable is false when no comparison could actually be performed — i.e. the condition has no
// recognizable IN list and no comparison operator that can be located at top level (for example an
// operator that only exists inside quoted text or inside unbalanced placeholders). In that case the
// boolean result is meaningless and the caller (evaluateConditionalExpression) resolves the whole
// CONDITION to blank and logs an error, instead of silently taking the false branch.
func (ep *ExpressionProcessor) evaluateSimpleConditionChecked(condition string, masterDTO *common_dto.MasterDTO, serviceNameMap map[string]*models.EsaLog) (result bool, evaluable bool) {
	condition = strings.TrimSpace(condition)

	// IN operator: "leftValue IN ('a','b',...)" — case-sensitive membership check
	if strings.Contains(condition, " IN ") {
		parts := strings.SplitN(condition, " IN ", 2)
		if len(parts) == 2 {
			leftPart := strings.TrimSpace(parts[0])
			listPart := strings.TrimSpace(parts[1])
			if listPart != "" && (strings.HasPrefix(listPart, "(") && strings.HasSuffix(listPart, ")")) {
				listPart = strings.Trim(listPart, "()")
				allowedValues := make([]string, 0)
				for _, s := range strings.Split(listPart, ",") {
					v := strings.TrimSpace(s)
					v = strings.Trim(v, "'\"")
					if v != "" {
						allowedValues = append(allowedValues, v)
					}
				}
				if len(allowedValues) > 0 {
					leftValue := ep.processUnifiedParameter(leftPart, masterDTO, serviceNameMap)
					leftValue = strings.Trim(leftValue, "'\"")
					leftValue = stripInternalTypeMarker(leftValue)
					if debugPipeline {
						fmt.Printf("[SIMPLE_COND] IN check: leftPart=%q resolved=%q allowedValues=%v\n",
							leftPart, leftValue, allowedValues)
					}
					for _, allowed := range allowedValues {
						if leftValue == allowed {
							return true, true
						}
					}
					return false, true
				}
			}
		}
	}

	// Support different comparison operators; resolve both sides as expressions ({{ }}, <>, (()))
	operators := []string{">=", "<=", "==", "!=", ">", "<"}

	// operatorLocated records whether any comparison operator could actually be located at top level.
	// A located operator (even one with a degenerate/empty operand) means a real comparison was
	// attempted, so the condition is evaluable — this preserves the legacy behavior where e.g.
	// "((AbsentService)) != ''" (which resolves to "!= ''") yields a definite false. Only a condition
	// with comparison intent but NO locatable operator is treated as unevaluable (BUG-2).
	operatorLocated := false

	for _, op := range operators {
		if strings.Contains(condition, op) {
			// Find the operator position, avoiding conflicts with placeholders
			opIndex := ep.findOperatorPosition(condition, op)
			if opIndex == -1 {
				continue
			}
			operatorLocated = true

			// Split manually at the operator position
			leftPart := condition[:opIndex]
			rightPart := condition[opIndex+len(op):]

			if leftPart != "" && rightPart != "" {
				leftValue := ep.processUnifiedParameter(strings.TrimSpace(leftPart), masterDTO, serviceNameMap)
				rightValue := ep.processUnifiedParameter(strings.TrimSpace(rightPart), masterDTO, serviceNameMap)
				rightValue = strings.Trim(rightValue, "'\"")

				leftValue = stripInternalTypeMarker(leftValue)
				rightValue = stripInternalTypeMarker(rightValue)

				// Null-token handling: config wrote `== null` / `!= null` (UNQUOTED, case-insensitive).
				// The left side is null when it resolves to blank (nil / absent field / empty string).
				// Empty collections ({} / []) are NOT null here — configs match those explicitly via
				// == '{}' / == '[]'. A quoted 'null' is a literal string and falls through to normal
				// comparison below.
				if (op == "==" || op == "!=") && strings.EqualFold(strings.TrimSpace(rightPart), "null") {
					leftBlank := strings.TrimSpace(leftValue) == ""
					if op == "==" {
						return leftBlank, true
					}
					return !leftBlank, true
				}

				// For == '' / != '', treat empty JSON "{}" and "[]" as empty so ((Service)) != '' is false when body is {}
				if rightValue == "" && (op == "==" || op == "!=") {
					if debugPipeline {
						fmt.Printf("[SIMPLE_COND] Before normalize: leftValue=%q\n", debugTruncate(leftValue, 100))
					}
					leftValue = ep.normalizeEmptyForComparison(leftValue)
				}
				if debugPipeline {
					fmt.Printf("[SIMPLE_COND] op=%s left=%q right=%q\n", op, debugTruncate(leftValue, 100), debugTruncate(rightValue, 100))
				}

				// Try numeric comparison first
				leftNum, leftErr := strconv.ParseFloat(leftValue, 64)
				rightNum, rightErr := strconv.ParseFloat(rightValue, 64)

				if leftErr == nil && rightErr == nil {
					// Numeric comparison
					switch op {
					case ">=":
						return leftNum >= rightNum, true
					case "<=":
						return leftNum <= rightNum, true
					case "==":
						return leftNum == rightNum, true
					case "!=":
						return leftNum != rightNum, true
					case ">":
						return leftNum > rightNum, true
					case "<":
						return leftNum < rightNum, true
					}
				} else {
					// Relational comparison with an empty/unresolved operand -> not-satisfied
					// (false), instead of lexical comparison where "" < "30" would be true.
					// Equality (==/!=) is unaffected; non-empty operands keep lexical comparison.
					if (op == ">" || op == "<" || op == ">=" || op == "<=") && (leftValue == "" || rightValue == "") {
						return false, true
					}
					// String comparison
					result := false
					switch op {
					case "==":
						result = leftValue == rightValue
					case "!=":
						result = leftValue != rightValue
					case ">":
						result = leftValue > rightValue
					case "<":
						result = leftValue < rightValue
					case ">=":
						result = leftValue >= rightValue
					case "<=":
						result = leftValue <= rightValue
					}
					return result, true
				}
			}
			break
		}
	}

	// Nothing produced a comparison result above. Distinguish three cases:
	//   (a) An operator WAS located but an operand was empty (e.g. "((AbsentService)) != ''" -> "!= ''").
	//       This is a real, degenerate comparison -> definite false, and it IS evaluable. Preserving
	//       this keeps the fail-closed behavior for absent/failed services (gate false -> else branch).
	//   (b) No operator could be located but the text has genuine comparison intent (an operator or
	//       IN list trapped inside unbalanced brackets, or otherwise unparseable). Report
	//       evaluable=false so the CONDITION resolves to blank (BUG-2) instead of silently returning
	//       the false branch.
	//   (c) A bare value with no comparison operator at all (e.g. a nested expression that resolved to
	//       empty). Preserve legacy semantics: a definite false result that IS evaluable.
	if operatorLocated {
		return false, true
	}
	if ep.conditionHasComparisonIntent(condition) {
		return false, false
	}
	return false, true
}

// conditionHasComparisonIntent reports whether condition textually contains a comparison operator
// (==, !=, >=, <=, >, <) or an IN list OUTSIDE of quoted string literals and <...> angle
// placeholders. It deliberately ignores bracket depth, so an operator that exists but cannot be
// located at top level (e.g. trapped inside an unbalanced parenthesis group) still counts as intent.
// This lets evaluateSimpleConditionChecked separate a malformed comparison (unevaluable) from a
// bare/operator-less condition (legacy false), without misreading the '<' '>' of a placeholder.
func (ep *ExpressionProcessor) conditionHasComparisonIntent(condition string) bool {
	if strings.Contains(condition, " IN ") {
		return true
	}
	inAngle := false
	inQuotes := false
	var quoteChar byte
	for i := 0; i < len(condition); i++ {
		c := condition[i]
		if inQuotes {
			if c == '\\' && i+1 < len(condition) {
				i++
				continue
			}
			if c == quoteChar {
				inQuotes = false
				quoteChar = 0
			}
			continue
		}
		if c == '\'' || c == '"' {
			inQuotes = true
			quoteChar = c
			continue
		}
		// Angle placeholder <...>: opened only when '<' is followed by a letter/dot (matches
		// findOperatorPosition), so a relational '<'/'>' is not mistaken for a placeholder.
		if c == '<' && !inAngle {
			if i+1 < len(condition) && (ep.isLetter(condition[i+1]) || condition[i+1] == '.') {
				inAngle = true
				continue
			}
		} else if c == '>' && inAngle {
			inAngle = false
			continue
		}
		if inAngle {
			continue
		}
		switch c {
		case '=':
			if i+1 < len(condition) && condition[i+1] == '=' {
				return true // ==
			}
		case '!':
			if i+1 < len(condition) && condition[i+1] == '=' {
				return true // !=
			}
		case '>', '<':
			return true // relational (>, <, >=, <=) outside any placeholder
		}
	}
	return false
}

// ============================================================================
// CUSTOM EXPRESSIONS {{CUSTOM:...}}
// ============================================================================

// evaluateCustomExpression handles custom business logic
func (ep *ExpressionProcessor) evaluateCustomExpression(expression string, masterDTO *common_dto.MasterDTO, serviceNameMap map[string]*models.EsaLog) string {
	// Handle nested expressions properly by parsing carefully
	methodName, params := ep.parseCustomLogicExpression(expression)
	if methodName == "" {
		return ""
	}
	params = ep.normalizeCustomParams(methodName, params)
	if methodName == "stringifyJSON" {
		return ep.customStringifyJSON(params, masterDTO, serviceNameMap)
	}

	// Process parameters
	processedParams := make([]string, len(params))
	for i, param := range params {
		param = strings.TrimSpace(param)

		if strings.HasPrefix(param, "'") && strings.HasSuffix(param, "'") {
			// Literal string. Reverse the single-quote wrap escaping (see unescapeSingleQuoteWrapped)
			// so a nested result containing apostrophes (e.g. JSON like B'JOHN') stays valid JSON
			// for downstream parsing such as getJsonPath's json.Unmarshal. Without this, an embedded
			// "\'" is an illegal JSON escape and the parse fails, yielding an empty result.
			processedParams[i] = unescapeSingleQuoteWrapped(param[1 : len(param)-1])
		} else if strings.HasPrefix(param, "{{") && strings.HasSuffix(param, "}}") {
			// Nested expression - process recursively
			inner := param[2 : len(param)-2]
			processedParams[i] = ep.evaluateExpression(inner, masterDTO, serviceNameMap)
		} else if ep.isTopLevelFallback(param) {
			// Fallback expression
			processedParams[i] = ep.evaluateExpressionFallback(param, masterDTO, serviceNameMap)
		} else if ep.isNumericLiteral(param) {
			// Numeric literal
			processedParams[i] = param
		} else {
			// Resolve placeholders: <Object.field> (SF/DB), ((Service.field)) (service response), and fallback chains
			processedParams[i] = ep.processUnifiedParameter(param, masterDTO, serviceNameMap)
		}
	}

	// Execute custom methods
	switch methodName {
	case "generateId":
		return ep.customGenerateId(processedParams)
	case "calculateAge":
		return ep.customCalculateAge(processedParams)
	case "validatePAN":
		return ep.customValidatePAN(processedParams)
	case "formatPhone":
		return ep.customFormatPhone(processedParams)
	case "getCurrentTimestamp":
		return ep.customGetCurrentTimestamp(processedParams)
	case "cleanSpecialChars":
		return ep.customCleanSpecialChars(processedParams)
	case "takeLast":
		return ep.customTakeLast(processedParams)
	case "sha256Hash":
		return ep.customSha256Hash(processedParams)
	case "dateWithinDays":
		return ep.customDateWithinDays(processedParams)
	case "daysSince":
		return ep.customDaysSince(processedParams)
	case "monthsSince":
		return ep.customMonthsSince(processedParams)
	case "getNumericValue":
		return ep.customGetNumericValue(processedParams)
	case "calculateFOIR":
		return ep.customCalculateFOIR(processedParams)
	case "resolveBureauStateCode":
		return ep.customResolveBureauStateCode(processedParams)
	case "splitName":
		return ep.customSplitName(processedParams)
	case "mapGender":
		return ep.customMapGender(processedParams)
	case "mapDocumentType":
		return ep.customMapDocumentType(processedParams)
	case "base64Encode":
		return ep.customBase64Encode(processedParams)
	case "getJsonPath":
		return ep.customGetJsonPath(processedParams)
	case "normalizeJsonNan":
		return ep.customNormalizeJsonNan(processedParams)
	case "joinDistinctField":
		return ep.customJoinDistinctField(processedParams)
	case "extractPincode", "getOfficePincode":
		// getOfficePincode is an alias kept for existing DB configs that pass an address plus an
		// explicit pincode field; both resolve through the same left-to-right search.
		return ep.customExtractPincode(processedParams)
	default:
		return ""
	}
}

func (ep *ExpressionProcessor) customStringifyJSON(params []string, masterDTO *common_dto.MasterDTO, serviceNameMap map[string]*models.EsaLog) string {
	if len(params) < 1 {
		return ""
	}

	if len(params) == 1 {
		return ep.stringifyJSONParam(params[0], masterDTO, serviceNameMap)
	}

	parts := ep.splitFallbackExpression(strings.Join(params, ":"))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		result := ep.stringifyJSONParam(part, masterDTO, serviceNameMap)
		if result != "" {
			return result
		}
	}

	return ""
}

func (ep *ExpressionProcessor) stringifyJSONParam(param string, masterDTO *common_dto.MasterDTO, serviceNameMap map[string]*models.EsaLog) string {
	param = strings.TrimSpace(param)
	if param == "" {
		return ""
	}

	if (strings.HasPrefix(param, "'") && strings.HasSuffix(param, "'")) ||
		(strings.HasPrefix(param, "\"") && strings.HasSuffix(param, "\"")) {
		return ep.normalizeJSONString(param[1 : len(param)-1])
	}

	if strings.HasPrefix(param, "{{") && strings.HasSuffix(param, "}}") {
		inner := strings.TrimSpace(param[2 : len(param)-2])
		return ep.normalizeJSONString(ep.evaluateExpression(inner, masterDTO, serviceNameMap))
	}

	if ep.isTopLevelFallback(param) {
		for _, part := range ep.splitFallbackExpression(param) {
			result := ep.stringifyJSONParam(strings.TrimSpace(part), masterDTO, serviceNameMap)
			if result != "" {
				return result
			}
		}
		return ""
	}

	if strings.HasPrefix(param, "((") && strings.HasSuffix(param, "))") {
		raw := ep.getServiceResponseValueRaw(param[2:len(param)-2], serviceNameMap)
		return ep.stringifyJSONValue(raw)
	}

	if strings.HasPrefix(param, "<") && strings.HasSuffix(param, ">") {
		return ep.normalizeJSONString(ep.getDbFieldValue(param[1:len(param)-1], masterDTO))
	}

	return ep.normalizeJSONString(ep.processUnifiedParameter(param, masterDTO, serviceNameMap))
}

func (ep *ExpressionProcessor) stringifyJSONValue(value interface{}) string {
	if value == nil {
		return ""
	}

	switch v := value.(type) {
	case string:
		return ep.normalizeJSONString(v)
	default:
		bytes, err := json.Marshal(v)
		if err != nil {
			return ""
		}
		return string(bytes)
	}
}

func (ep *ExpressionProcessor) normalizeJSONString(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}

	if strings.HasPrefix(value, "__JSON_PARSE__:") {
		return strings.TrimPrefix(value, "__JSON_PARSE__:")
	}

	var parsed interface{}
	if err := json.Unmarshal([]byte(value), &parsed); err == nil {
		bytes, err := json.Marshal(parsed)
		if err == nil {
			return string(bytes)
		}
	}

	return value
}

// parseCustomLogicExpression carefully parses custom logic expressions to handle nested content
func (ep *ExpressionProcessor) parseCustomLogicExpression(expression string) (string, []string) {
	// Find the first colon to separate method name
	firstColonIndex := strings.Index(expression, ":")
	if firstColonIndex == -1 {
		return strings.TrimSpace(expression), []string{}
	}

	methodName := strings.TrimSpace(expression[:firstColonIndex])
	remainder := expression[firstColonIndex+1:]

	// Parse parameters while respecting nested {{...}} structures.
	// quoteChar tracks which quote type opened the current quoted region so that
	// JSON content inside single-quoted params (e.g. '{"key":"val:ue"}') does not
	// have its inner double-quotes incorrectly toggle the inQuotes state. A '"'
	// inside a '...' region must not close the region; only the matching '\'' can.
	params := []string{}
	// strings.Builder replaces the previous `current += string(char)` accumulation, which was
	// O(n^2) (each append reallocated and copied the whole buffer) and made multi-MB CUSTOM
	// arguments (e.g. a 3.7MB CRIF blob passed to getJsonPath) hang the request CPU-bound.
	// IMPORTANT: WriteRune(rune(char)) reproduces the EXACT bytes of the old `string(char)`
	// (which encodes the byte as a code point), preserving prior behavior for non-ASCII input.
	// Do NOT switch to WriteByte here — that would change output for bytes > 0x7F.
	var current strings.Builder
	depth := 0
	inQuotes := false
	var quoteChar byte

	for i := 0; i < len(remainder); i++ {
		char := remainder[i]

		switch char {
		case '\\':
			// Inside a quoted region, a backslash escapes the next char (\' or \\ from the
			// single-quote wrap). Emit both bytes verbatim and skip interpretation so an escaped
			// apostrophe does not close the quote and inner ':' are not mistaken for separators.
			current.WriteRune(rune(char))
			if inQuotes && i+1 < len(remainder) {
				i++
				current.WriteRune(rune(remainder[i]))
			}
		case '\'', '"':
			if !inQuotes {
				inQuotes = true
				quoteChar = char
			} else if char == quoteChar {
				inQuotes = false
				quoteChar = 0
			}
			current.WriteRune(rune(char))
		case '{':
			if !inQuotes && i < len(remainder)-1 && remainder[i+1] == '{' {
				// Found opening {{
				depth++
				current.WriteString("{{")
				i++ // Skip the next '{'
			} else {
				current.WriteRune(rune(char))
			}
		case '}':
			if !inQuotes && i < len(remainder)-1 && remainder[i+1] == '}' {
				// Found closing }}
				depth--
				current.WriteString("}}")
				i++ // Skip the next '}'
			} else {
				current.WriteRune(rune(char))
			}
		case ':':
			if !inQuotes && depth == 0 {
				// This is a parameter separator at top level
				params = append(params, strings.TrimSpace(current.String()))
				current.Reset()
			} else {
				current.WriteRune(rune(char))
			}
		default:
			current.WriteRune(rune(char))
		}
	}

	// Add the last parameter (guard on raw, untrimmed length to match prior `current != ""`).
	if current.Len() > 0 {
		params = append(params, strings.TrimSpace(current.String()))
	}

	return methodName, params
}

func (ep *ExpressionProcessor) normalizeCustomParams(methodName string, params []string) []string {
	switch methodName {
	case "generateId", "calculateAge", "validatePAN", "formatPhone", "getCurrentTimestamp",
		"cleanSpecialChars", "sha256Hash", "daysSince", "getNumericValue", "base64Encode", "normalizeJsonNan":
		if len(params) > 1 {
			return []string{strings.Join(params, ":")}
		}
	case "dateWithinDays":
		return normalizeDateWithinDaysParams(params)
	case "monthsSince":
		return ep.normalizeDateAndOptionalFormatParams(params)
	}
	return params
}

func normalizeDateWithinDaysParams(params []string) []string {
	if len(params) <= 2 {
		return params
	}

	tailCount := 0
	for i := len(params) - 1; i >= 0 && tailCount < 2; i-- {
		if _, err := strconv.Atoi(strings.TrimSpace(params[i])); err == nil {
			tailCount++
			continue
		}
		break
	}

	if tailCount == 0 || len(params)-tailCount <= 0 {
		return []string{strings.Join(params, ":")}
	}

	normalized := []string{strings.Join(params[:len(params)-tailCount], ":")}
	normalized = append(normalized, params[len(params)-tailCount:]...)
	return normalized
}

func (ep *ExpressionProcessor) normalizeDateAndOptionalFormatParams(params []string) []string {
	if len(params) <= 2 {
		return params
	}

	joined := strings.Join(params, ":")
	trimmedJoined := strings.TrimSpace(joined)
	if trimmedJoined == "" {
		return []string{joined}
	}

	layoutCache := make(map[string]string, 8)
	getLayout := func(formatPart string) string {
		if layout, exists := layoutCache[formatPart]; exists {
			return layout
		}
		layout := ep.translateDateFormat(formatPart)
		layoutCache[formatPart] = layout
		return layout
	}

	bestValue := ""
	bestFormat := ""
	for i := strings.IndexByte(joined, ':'); i >= 0; {
		valuePart := strings.TrimSpace(joined[:i])
		formatPart := strings.TrimSpace(joined[i+1:])
		if valuePart == "" || formatPart == "" {
			next := strings.IndexByte(joined[i+1:], ':')
			if next < 0 {
				break
			}
			i = i + 1 + next
			continue
		}
		layout := getLayout(formatPart)
		if _, err := time.Parse(layout, valuePart); err == nil && len(formatPart) > len(bestFormat) {
			bestValue = valuePart
			bestFormat = formatPart
		}
		next := strings.IndexByte(joined[i+1:], ':')
		if next < 0 {
			break
		}
		i = i + 1 + next
	}

	if bestValue != "" && bestFormat != "" {
		return []string{bestValue, bestFormat}
	}

	return []string{joined}
}

// ============================================================================
// NUMERIC/BOOLEAN/JSON EXPRESSIONS
// ============================================================================

// evaluateNumericExpression handles numeric type conversion with unified parameter processing
func (ep *ExpressionProcessor) evaluateNumericExpression(expression string, masterDTO *common_dto.MasterDTO, serviceNameMap map[string]*models.EsaLog) string {
	// Use unified parameter processing for consistent fallback and nested expression support
	resolvedValue := ep.processUnifiedParameter(expression, masterDTO, serviceNameMap)
	if resolvedValue == "" {
		return "__NULL_NUMERIC__" // Special marker for null handling
	}

	// Try to parse as number
	if val, err := strconv.ParseFloat(resolvedValue, 64); err == nil {
		// Return with special prefix to indicate this should be converted to number
		if val == float64(int(val)) {
			return fmt.Sprintf("__NUMERIC_INT__:%d", int(val))
		}
		return fmt.Sprintf("__NUMERIC_FLOAT__:%g", val)
	}

	return "__NULL_NUMERIC__" // Special marker for null handling
}

// evaluateBooleanExpression handles boolean type conversion with unified parameter processing
func (ep *ExpressionProcessor) evaluateBooleanExpression(expression string, masterDTO *common_dto.MasterDTO, serviceNameMap map[string]*models.EsaLog) string {
	// Use unified parameter processing for consistent fallback and nested expression support
	innerValue := ep.processUnifiedParameter(expression, masterDTO, serviceNameMap)
	if innerValue == "" {
		return "__BOOLEAN_FALSE__" // Empty string should be false, not null
	}

	switch strings.ToLower(strings.TrimSpace(innerValue)) {
	case "true", "1", "yes", "on", "enabled":
		return "__BOOLEAN_TRUE__"
	default:
		// All other values (including "false", "0", "no", "off", "disabled", and any other value) should be false
		return "__BOOLEAN_FALSE__"
	}
}

// evaluateJsonExpression handles JSON object embedding
func (ep *ExpressionProcessor) evaluateJsonExpression(expression string, masterDTO *common_dto.MasterDTO, serviceNameMap map[string]*models.EsaLog) string {
	// Handle service response placeholders specially
	if strings.HasPrefix(expression, "((") && strings.HasSuffix(expression, "))") {
		servicePlaceholder := expression[2 : len(expression)-2]

		// Get the raw value (not string) for proper JSON handling
		rawValue := ep.getServiceResponseValueRaw(servicePlaceholder, serviceNameMap)

		if rawValue != nil {
			// Check if it's a string that might be JSON
			if str, ok := rawValue.(string); ok {
				// Try to parse as JSON first
				var jsonValue interface{}
				if err := json.Unmarshal([]byte(str), &jsonValue); err == nil {
					// It was a JSON string, return with special marker for parsing
					if jsonBytes, err := json.Marshal(jsonValue); err == nil {
						return fmt.Sprintf("__JSON_PARSE__:%s", string(jsonBytes))
					}
				}
				// Not JSON, marshal the string as-is with marker
				if jsonBytes, err := json.Marshal(str); err == nil {
					return fmt.Sprintf("__JSON_PARSE__:%s", string(jsonBytes))
				}
			} else {
				// Marshal the raw value to JSON with marker
				if jsonBytes, err := json.Marshal(rawValue); err == nil {
					return fmt.Sprintf("__JSON_PARSE__:%s", string(jsonBytes))
				}
			}
		}
		return "__JSON_PARSE__:{}"
	}

	// Handle other expressions with unified parameter processing
	resolvedValue := ep.processUnifiedParameter(expression, masterDTO, serviceNameMap)
	if resolvedValue == "" {
		return "__JSON_PARSE__:{}"
	}

	// Try to parse the resolved value as JSON
	var jsonValue interface{}
	if err := json.Unmarshal([]byte(resolvedValue), &jsonValue); err == nil {
		// It's already valid JSON, return with marker
		return fmt.Sprintf("__JSON_PARSE__:%s", resolvedValue)
	}

	// Marshal as JSON string with marker
	marshaled, err := json.Marshal(resolvedValue)
	if err != nil {
		return "__JSON_PARSE__:{}"
	}
	return fmt.Sprintf("__JSON_PARSE__:%s", string(marshaled))
}

// evaluateArrayExpression handles array transformations
func (ep *ExpressionProcessor) evaluateArrayExpression(expression string, masterDTO *common_dto.MasterDTO, serviceNameMap map[string]*models.EsaLog) string {
	// CRITICAL: Use brace-aware parsing to find the first colon at depth 0
	// This prevents splitting on colons inside nested ARRAY expressions like:
	// {{ARRAY:((Service.body)):transform-only:field->key}}:merge
	// Without this, strings.Split would break nested expressions

	var sourcePath string
	var operation string
	var operationParams []string

	// Find the first colon at depth 0 (not inside nested {{...}} or JSON arrays [...])
	firstColonIndex := -1
	braceDepth := 0   // Tracks {{ }} depth
	bracketDepth := 0 // Tracks [ ] depth (for JSON arrays)
	inQuotes := false
	var quoteChar byte

findFirstColon:
	for i := 0; i < len(expression); i++ {
		char := expression[i]
		switch char {
		case '\\':
			// Skip escaped char inside a quoted region (\' / \\ from the single-quote wrap) so an
			// escaped apostrophe does not close the quote and inner ':' are not seen as separators.
			if inQuotes && i+1 < len(expression) {
				i++
			}
		case '"', '\'':
			if !inQuotes {
				inQuotes = true
				quoteChar = char
			} else if char == quoteChar {
				inQuotes = false
			}
		case '{':
			if !inQuotes {
				if i < len(expression)-1 && expression[i+1] == '{' {
					// Double brace {{ - increment brace depth
					braceDepth++
					i++ // Skip next '{'
				} else {
					// Single brace { - increment bracket depth (could be JSON object)
					bracketDepth++
				}
			}
		case '}':
			if !inQuotes {
				if i < len(expression)-1 && expression[i+1] == '}' {
					// Double brace }} - decrement brace depth
					braceDepth--
					i++ // Skip next '}'
				} else {
					// Single brace } - decrement bracket depth
					bracketDepth--
				}
			}
		case '[':
			if !inQuotes {
				bracketDepth++ // JSON array start
			}
		case ']':
			if !inQuotes {
				bracketDepth-- // JSON array end
			}
		case ':':
			// Only consider colon if we're at top level (no nested braces or brackets)
			if !inQuotes && braceDepth == 0 && bracketDepth == 0 {
				// Skip colon if it's part of __ARRAY_PARSE__: or __JSON_PARSE__: marker
				if i >= 15 && expression[i-15:i] == "__ARRAY_PARSE__" {
					continue
				}
				if i >= 14 && expression[i-14:i] == "__JSON_PARSE__" {
					continue
				}
				// Found first colon at top level that separates source from operation
				firstColonIndex = i
				break findFirstColon
			}
		}
	}

	if firstColonIndex == -1 {
		// No colon found - entire expression is the source
		sourcePath = strings.TrimSpace(expression)
		operation = ""
	} else {
		sourcePath = strings.TrimSpace(expression[:firstColonIndex])
		remainder := strings.TrimSpace(expression[firstColonIndex+1:])

		// Check if remainder is "merge" or starts with "merge:"
		if remainder == "merge" {
			operation = "merge"
		} else if strings.HasPrefix(remainder, "merge:") {
			operation = "merge"
			// Parse operations after merge (e.g., merge:transform-only:...)
			operationParams = []string{remainder[6:]}
		} else {
			// Find next colon at depth 0 for operation params
			secondColonIndex := -1
			braceDepth := 0
			inQuotes := false

		findSecondColon:
			for i := 0; i < len(remainder); i++ {
				char := remainder[i]
				switch char {
				case '\\':
					if inQuotes && i+1 < len(remainder) {
						i++ // skip escaped char (\' / \\) inside a quoted region
					}
				case '"', '\'':
					if !inQuotes {
						inQuotes = true
						quoteChar = char
					} else if char == quoteChar {
						inQuotes = false
					}
				case '{':
					if !inQuotes && i < len(remainder)-1 && remainder[i+1] == '{' {
						braceDepth++
						i++
					}
				case '}':
					if !inQuotes && i < len(remainder)-1 && remainder[i+1] == '}' {
						braceDepth--
						i++
					}
				case ':':
					if !inQuotes && braceDepth == 0 {
						secondColonIndex = i
						break findSecondColon
					}
				}
			}

			if secondColonIndex == -1 {
				// No second colon - remainder is the operation
				operation = remainder
			} else {
				operation = strings.TrimSpace(remainder[:secondColonIndex])
				paramsStr := strings.TrimSpace(remainder[secondColonIndex+1:])
				operationParams = []string{paramsStr}
			}
		}
	}

	if debugPipeline {
		fmt.Printf("[ARRAY_EVAL] sourcePath=%s operation=%s operationParams=%v\n",
			debugTruncate(sourcePath, 200), operation, operationParams)
	}

	var sourceArray []interface{}

	// Check if this is a merge operation with multiple comma-separated sources
	if operation == "merge" && strings.Contains(sourcePath, ",") {
		// Split sources by comma while respecting nested expressions
		sourcePaths := ep.splitCommaSeparatedSources(sourcePath)
		var arraysToMerge [][]interface{}

		for _, src := range sourcePaths {
			src = strings.TrimSpace(src)
			if src == "" {
				continue
			}
			arr := ep.getArrayFromSource(src, masterDTO, serviceNameMap)
			if len(arr) > 0 {
				arraysToMerge = append(arraysToMerge, arr)
			}
		}

		// Merge all arrays
		sourceArray = ep.mergeArrays(arraysToMerge)
	} else {
		// Single source - strip optional @NUMERIC suffix for max/min comparison mode
		numericCompare := false
		cleanedSourcePath := strings.TrimSpace(sourcePath)
		if strings.HasSuffix(cleanedSourcePath, "@NUMERIC") {
			cleanedSourcePath = strings.TrimSpace(strings.TrimSuffix(cleanedSourcePath, "@NUMERIC"))
			numericCompare = true
		}
		sourceArray = ep.getArrayFromSource(cleanedSourcePath, masterDTO, serviceNameMap)
		// Pass @NUMERIC to max/min so they compare numerically; preserve optional field name (e.g. :uan)
		if (operation == "max" || operation == "min") && numericCompare {
			p := []string{"@NUMERIC"}
			if len(operationParams) > 0 {
				existing := strings.TrimSpace(operationParams[0])
				if existing != "" && existing != "@NUMERIC" {
					p = append(p, existing)
				}
			}
			operationParams = p
		}
	}

	if debugPipeline {
		sourceJSON, _ := json.Marshal(sourceArray)
		fmt.Printf("[ARRAY_EVAL] sourceArray (len=%d): %s\n", len(sourceArray), debugTruncate(string(sourceJSON), 300))
	}

	var result []interface{}

	// effectiveOperation is the operation whose OUTPUT `result` actually holds. For a plain
	// "op" it equals operation; for a chained "merge:<op>" it becomes the chained <op>. The
	// scalar-rendering checks below (count / max / min) must key off this, not the top-level
	// operation — otherwise "merge:count" computes the count correctly but is rendered as an
	// __ARRAY_PARSE__:[N] array because the top-level operation is "merge" (the pre-existing bug).
	effectiveOperation := operation

	// If no operation specified, return array as-is
	if operation == "" {
		result = sourceArray
	} else if operation == "merge" {
		// Merge operation already handled above, but check if there are subsequent operations
		result = sourceArray
		// Check if there are more operations after merge (e.g., merge:transform-only:...)
		if len(operationParams) > 0 && operationParams[0] != "" {
			// Parse the operation after merge using brace-aware parsing
			nextOpStr := operationParams[0]
			nextColonIndex := -1
			braceDepth := 0
			inQuotes = false

		findNextColon:
			for i := 0; i < len(nextOpStr); i++ {
				char := nextOpStr[i]
				switch char {
				case '\\':
					if inQuotes && i+1 < len(nextOpStr) {
						i++ // skip escaped char (\' / \\) inside a quoted region
					}
				case '"', '\'':
					if !inQuotes {
						inQuotes = true
						quoteChar = char
					} else if char == quoteChar {
						inQuotes = false
					}
				case '{':
					if !inQuotes && i < len(nextOpStr)-1 && nextOpStr[i+1] == '{' {
						braceDepth++
						i++
					}
				case '}':
					if !inQuotes && i < len(nextOpStr)-1 && nextOpStr[i+1] == '}' {
						braceDepth--
						i++
					}
				case ':':
					if !inQuotes && braceDepth == 0 {
						nextColonIndex = i
						break findNextColon
					}
				}
			}

			if nextColonIndex != -1 {
				nextOperation := strings.TrimSpace(nextOpStr[:nextColonIndex])
				nextParams := []string{nextOpStr[nextColonIndex+1:]}
				result = ep.applyArrayOperation(result, nextOperation, nextParams)
				effectiveOperation = nextOperation
			} else {
				// No colon - entire string is the operation
				nextOperation := strings.TrimSpace(nextOpStr)
				result = ep.applyArrayOperation(result, nextOperation, []string{})
				effectiveOperation = nextOperation
			}
		}
	} else {
		// Process with specified operation
		result = ep.applyArrayOperation(sourceArray, operation, operationParams)
	}

	// count returns the element count as a single scalar number string (no __ARRAY_PARSE__ wrapper).
	// Keyed on effectiveOperation so chained forms like "merge:count" also render the scalar.
	if effectiveOperation == "count" {
		if len(result) == 1 {
			if n, ok := result[0].(int); ok {
				return fmt.Sprintf("%d", n)
			}
		}
		return "0"
	}

	// max/min return a single scalar; output as string without __ARRAY_PARSE__ wrapper.
	// Keyed on effectiveOperation so chained forms like "merge:max" also render the scalar.
	if effectiveOperation == "max" || effectiveOperation == "min" {
		if len(result) == 0 {
			return ""
		}
		return ep.arrayElementToString(result[0])
	}

	// Marshal result back to JSON string
	resultJSON, err := json.Marshal(result)
	if err != nil {
		return "__ARRAY_PARSE__:[]"
	}
	return fmt.Sprintf("__ARRAY_PARSE__:%s", string(resultJSON))
}

// getArrayFromSource extracts an array from a single source path
func (ep *ExpressionProcessor) getArrayFromSource(sourcePath string, masterDTO *common_dto.MasterDTO, serviceNameMap map[string]*models.EsaLog) []interface{} {
	var sourceArray []interface{}

	// CRITICAL: Check if sourcePath is already an ARRAY_PARSE marker (from nested ARRAY expressions)
	// This happens when nested ARRAY expressions return __ARRAY_PARSE__:[...] and are used as sources
	if strings.HasPrefix(sourcePath, "__ARRAY_PARSE__:") {
		arrayStr := sourcePath[len("__ARRAY_PARSE__:"):]
		if err := json.Unmarshal([]byte(arrayStr), &sourceArray); err != nil {
			return []interface{}{}
		}
		return sourceArray
	}

	// Handle service response placeholders specially to get raw arrays
	if strings.HasPrefix(sourcePath, "((") && strings.HasSuffix(sourcePath, "))") {
		servicePlaceholder := sourcePath[2 : len(sourcePath)-2]
		rawValue := ep.getServiceResponseValueRaw(servicePlaceholder, serviceNameMap)

		if debugPipeline {
			rawJSON, _ := json.Marshal(rawValue)
			fmt.Printf("[ARRAY_SOURCE] servicePlaceholder=%s rawValue type=%T value=%s\n",
				servicePlaceholder, rawValue, debugTruncate(string(rawJSON), 200))
		}

		if rawValue == nil {
			return []interface{}{}
		}

		// Check if it's already an array
		if arr, ok := rawValue.([]interface{}); ok {
			sourceArray = arr
		} else {
			// Try to parse as JSON array if it's a string
			if str, ok := rawValue.(string); ok {
				if err := json.Unmarshal([]byte(str), &sourceArray); err != nil {
					return []interface{}{}
				}
			} else {
				return []interface{}{}
			}
		}
	} else if strings.HasPrefix(sourcePath, "<") && strings.HasSuffix(sourcePath, ">") {
		// Potential database placeholder - determine if it's object-only (<Object>) or field (<Object.Field>)
		inner := strings.TrimSpace(sourcePath[1 : len(sourcePath)-1])

		if strings.Contains(inner, ".") {
			// Regular object.field placeholder - use existing unified parameter processing
			sourceValue := ep.processUnifiedParameter(sourcePath, masterDTO, serviceNameMap)
			if sourceValue == "" {
				return []interface{}{}
			}

			if err := json.Unmarshal([]byte(sourceValue), &sourceArray); err != nil {
				return []interface{}{}
			}
		} else {
			// Object-only placeholder (<Object>) - fetch array of records from masterDTO
			sourceArray = ep.getObjectRecordsAsArray(inner, masterDTO)
		}
	} else {
		// Use unified parameter processing for consistent nested expression support
		// Trim whitespace but preserve structure for nested expressions
		trimmedPath := strings.TrimSpace(sourcePath)
		// If source is __JSON_PARSE__:... (from getJsonPath), parse the tail as array
		if strings.HasPrefix(trimmedPath, "__JSON_PARSE__:") {
			jsonStr := trimmedPath[len("__JSON_PARSE__:"):]
			if err := json.Unmarshal([]byte(jsonStr), &sourceArray); err == nil {
				return sourceArray
			}
		}
		// If source is already a literal JSON array (e.g. after replacement), parse directly
		if strings.HasPrefix(trimmedPath, "[") && strings.HasSuffix(trimmedPath, "]") {
			if err := json.Unmarshal([]byte(trimmedPath), &sourceArray); err == nil {
				return sourceArray
			}
		}
		sourceValue := ep.processUnifiedParameter(trimmedPath, masterDTO, serviceNameMap)

		if sourceValue == "" {
			return []interface{}{}
		}

		// Check if it's an ARRAY_PARSE marker (from nested ARRAY expressions)
		if strings.HasPrefix(sourceValue, "__ARRAY_PARSE__:") {
			arrayStr := sourceValue[len("__ARRAY_PARSE__:"):]
			if err := json.Unmarshal([]byte(arrayStr), &sourceArray); err != nil {
				return []interface{}{}
			}
		} else if strings.HasPrefix(sourceValue, "__JSON_PARSE__:") {
			// getJsonPath returns __JSON_PARSE__: for objects/arrays; use as array source
			jsonStr := sourceValue[len("__JSON_PARSE__:"):]
			if err := json.Unmarshal([]byte(jsonStr), &sourceArray); err != nil {
				return []interface{}{}
			}
		} else {
			// Try to parse as JSON array
			if err := json.Unmarshal([]byte(sourceValue), &sourceArray); err != nil {
				// If parsing fails, return empty array
				return []interface{}{}
			}
		}
	}

	return sourceArray
}

// mergeArrays merges multiple arrays into a single array
func (ep *ExpressionProcessor) mergeArrays(arrays [][]interface{}) []interface{} {
	result := make([]interface{}, 0)
	for _, arr := range arrays {
		result = append(result, arr...)
	}
	return result
}

// applyArrayOperation applies an array operation (transform, transform-only, filter, map) to an array
func (ep *ExpressionProcessor) applyArrayOperation(sourceArray []interface{}, operation string, params []string) []interface{} {
	switch operation {
	case "transform":
		if len(params) >= 1 {
			transformSpec := strings.TrimSpace(params[0])
			return ep.transformArray(sourceArray, ep.parseTransformMap(transformSpec), false)
		}
		return sourceArray
	case "transform-only":
		if len(params) >= 1 {
			transformSpec := strings.TrimSpace(params[0])
			return ep.transformArray(sourceArray, ep.parseTransformMap(transformSpec), true)
		}
		return sourceArray
	case "count":
		// Returns the number of elements as a single scalar (rendered by evaluateArrayExpression).
		return []interface{}{len(sourceArray)}
	case "filter":
		if len(params) >= 1 {
			condition := strings.TrimSpace(params[0])
			return ep.filterArray(sourceArray, condition)
		}
		return sourceArray
	case "map":
		if len(params) >= 1 {
			fieldsSpec := strings.TrimSpace(params[0])
			fields := strings.Split(fieldsSpec, ",")
			for i := range fields {
				fields[i] = strings.TrimSpace(fields[i])
			}
			return ep.mapArrayFields(sourceArray, fields)
		}
		return sourceArray
	case "max":
		numericCompare, fieldName := ep.parseMaxMinParams(params)
		arr := ep.arrayPluckField(sourceArray, fieldName)
		v := ep.arrayMax(arr, numericCompare)
		if v == nil {
			return []interface{}{}
		}
		return []interface{}{v}
	case "min":
		numericCompare, fieldName := ep.parseMaxMinParams(params)
		arr := ep.arrayPluckField(sourceArray, fieldName)
		v := ep.arrayMin(arr, numericCompare)
		if v == nil {
			return []interface{}{}
		}
		return []interface{}{v}
	default:
		return sourceArray
	}
}

// getObjectRecordsAsArray extracts an array of records for a given object from masterDTO data.
func (ep *ExpressionProcessor) getObjectRecordsAsArray(objectName string, masterDTO *common_dto.MasterDTO) []interface{} {
	if masterDTO == nil || masterDTO.Data == nil {
		return []interface{}{}
	}

	normalizedTarget := strings.ToLower(objectName)

	for existingKey, existingValue := range masterDTO.Data {
		if strings.EqualFold(existingKey, normalizedTarget) {
			dataMap := existingValue

			if records, ok := dataMap["records"]; ok {
				if recordsArray, ok := records.([]interface{}); ok {
					return recordsArray
				}
			}

			// If no records key, treat map entries as a single record
			return []interface{}{dataMap}
		}
	}

	return []interface{}{}
}

// ============================================================================
// DYNAMIC CONDITIONAL LOGIC FRAMEWORK
// ============================================================================

// evaluateConditionalLogicExpression handles complex business logic with multiple conditions and dynamic assignments
// Syntax: {{CONDITIONAL:input_value:condition1→result1:condition2→result2:default_result}}
// Advanced Syntax: {{CONDITIONAL:input_value:RULES:rule_definition}}
func (ep *ExpressionProcessor) evaluateConditionalLogicExpression(expression string, masterDTO *common_dto.MasterDTO, serviceNameMap map[string]*models.EsaLog) string {
	parts := ep.parseConditionalLogicParts(expression)
	if len(parts) < 2 {
		return ""
	}

	// Get input value using unified parameter processing
	inputValue := ep.processUnifiedParameter(parts[0], masterDTO, serviceNameMap)

	// Check if this is a RULES-based conditional
	if len(parts) >= 3 && strings.ToUpper(parts[1]) == "RULES" {
		return ep.evaluateRulesBasedConditional(inputValue, parts[2:], masterDTO, serviceNameMap)
	}

	// Standard conditional logic - evaluate each condition
	for i := 1; i < len(parts)-1; i++ {
		rule := parts[i]
		if ep.evaluateConditionalRule(rule, inputValue, masterDTO, serviceNameMap) {
			// Extract result from rule
			if result := ep.extractResultFromRule(rule, masterDTO, serviceNameMap); result != "" {
				return result
			}
		}
	}

	// Return default value (last part)
	if len(parts) > 1 {
		return ep.processUnifiedParameter(parts[len(parts)-1], masterDTO, serviceNameMap)
	}

	return ""
}

// parseConditionalLogicParts parses conditional logic expression while respecting nested expressions
func (ep *ExpressionProcessor) parseConditionalLogicParts(expression string) []string {
	parts := []string{}
	current := ""
	depth := 0
	inQuotes := false
	quoteChar := byte(0)

	for i := 0; i < len(expression); i++ {
		char := expression[i]

		switch char {
		case '\\':
			current += string(char)
			if inQuotes && i+1 < len(expression) {
				i++
				current += string(expression[i]) // keep escaped char (\' / \\) verbatim, don't toggle quote
			}
		case '\'', '"':
			if !inQuotes {
				inQuotes = true
				quoteChar = char
			} else if char == quoteChar {
				inQuotes = false
			}
			current += string(char)
		case '{':
			if !inQuotes && i < len(expression)-1 && expression[i+1] == '{' {
				depth++
				current += "{{"
				i++ // Skip next '{'
			} else {
				current += string(char)
			}
		case '}':
			if !inQuotes && i < len(expression)-1 && expression[i+1] == '}' {
				depth--
				current += "}}"
				i++ // Skip next '}'
			} else {
				current += string(char)
			}
		case ':':
			if !inQuotes && depth == 0 && !ep.isWithinISODateTime(expression, i) {
				// Top-level separator
				parts = append(parts, strings.TrimSpace(current))
				current = ""
			} else {
				current += string(char)
			}
		default:
			current += string(char)
		}
	}

	// Add final part
	if current != "" {
		parts = append(parts, strings.TrimSpace(current))
	}

	return parts
}

// evaluateRulesBasedConditional handles complex rule-based logic
func (ep *ExpressionProcessor) evaluateRulesBasedConditional(inputValue string, ruleParts []string, masterDTO *common_dto.MasterDTO, serviceNameMap map[string]*models.EsaLog) string {
	if len(ruleParts) == 0 {
		return ""
	}

	ruleDefinition := strings.Join(ruleParts, ":")

	// Parse and evaluate different rule types
	switch {
	case strings.Contains(ruleDefinition, "LENGTH"):
		return ep.evaluateLengthBasedRules(inputValue, ruleDefinition, masterDTO, serviceNameMap)
	case strings.Contains(ruleDefinition, "GENDER") || strings.Contains(ruleDefinition, "MAPPING"):
		return ep.evaluateMappingBasedRules(inputValue, ruleDefinition, masterDTO, serviceNameMap)
	case strings.Contains(ruleDefinition, "NAME") || strings.Contains(ruleDefinition, "SPLIT"):
		return ep.evaluateNameSplittingRules(inputValue, ruleDefinition, masterDTO, serviceNameMap)
	case strings.Contains(ruleDefinition, "DATE") || strings.Contains(ruleDefinition, "FORMAT"):
		return ep.evaluateDateFormattingRules(inputValue, ruleDefinition, masterDTO, serviceNameMap)
	case strings.Contains(ruleDefinition, "STATE") || strings.Contains(ruleDefinition, "BUREAU"):
		return ep.evaluateStateMappingRules(inputValue, ruleDefinition, masterDTO, serviceNameMap)
	default:
		return ep.evaluateGenericRules(inputValue, ruleDefinition, masterDTO, serviceNameMap)
	}
}

// evaluateLengthBasedRules handles document type assignment based on length
// Example: LENGTH:10->'01':12->'06':16->'04':'99'
func (ep *ExpressionProcessor) evaluateLengthBasedRules(inputValue, ruleDefinition string, masterDTO *common_dto.MasterDTO, serviceNameMap map[string]*models.EsaLog) string {
	// Extract length of input value
	inputLength := len(strings.TrimSpace(inputValue))

	// Parse length-based rules
	// Split by colon, but handle the rule format: LENGTH:10->'01':12->'06':16->'04':'99'
	ruleStr := strings.TrimPrefix(ruleDefinition, "LENGTH:")

	// Split by colon to get individual rules
	rules := strings.Split(ruleStr, ":")
	for _, rule := range rules {
		rule = strings.TrimSpace(rule)
		// Use keyboard-accessible arrow notation: ->
		if strings.Contains(rule, "->") {
			parts := strings.Split(rule, "->")
			if len(parts) == 2 {
				lengthStr := strings.TrimSpace(parts[0])
				result := strings.TrimSpace(parts[1])

				if expectedLength, err := strconv.Atoi(lengthStr); err == nil {
					if inputLength == expectedLength {
						// Remove quotes if present
						if strings.HasPrefix(result, "'") && strings.HasSuffix(result, "'") {
							result = result[1 : len(result)-1]
						}
						return ep.processUnifiedParameter(result, masterDTO, serviceNameMap)
					}
				}
			}
		}
	}

	// Return default value (last rule that doesn't contain ->)
	if len(rules) > 0 {
		defaultValue := rules[len(rules)-1]
		// If the last rule contains ->, it's not a default value
		if !strings.Contains(defaultValue, "->") {
			if strings.HasPrefix(defaultValue, "'") && strings.HasSuffix(defaultValue, "'") {
				defaultValue = defaultValue[1 : len(defaultValue)-1]
			}
			return ep.processUnifiedParameter(defaultValue, masterDTO, serviceNameMap)
		}
	}

	return ""
}

// evaluateMappingBasedRules handles gender and other value mappings
// Example: GENDER:MAPPING:M,m,Male->'2':F,f,Female->'1':'3'
func (ep *ExpressionProcessor) evaluateMappingBasedRules(inputValue, ruleDefinition string, masterDTO *common_dto.MasterDTO, serviceNameMap map[string]*models.EsaLog) string {
	inputValue = strings.TrimSpace(inputValue)

	// Parse mapping rules
	// Handle format: GENDER:MAPPING:M,m,Male,MALE->'2':F,f,Female,FEMALE->'1':'3'
	ruleStr := ruleDefinition

	// Remove prefixes
	if strings.HasPrefix(ruleStr, "GENDER:MAPPING:") {
		ruleStr = ruleStr[15:]
	} else if strings.HasPrefix(ruleStr, "MAPPING:") {
		ruleStr = ruleStr[8:]
	}

	// Split by colon to get individual mapping rules
	rules := strings.Split(ruleStr, ":")
	for _, rule := range rules {
		rule = strings.TrimSpace(rule)
		if strings.Contains(rule, "->") {
			parts := strings.Split(rule, "->")
			if len(parts) == 2 {
				conditions := strings.Split(parts[0], ",")
				result := strings.TrimSpace(parts[1])

				// Check if input matches any condition
				for _, condition := range conditions {
					condition = strings.TrimSpace(condition)
					if strings.EqualFold(inputValue, condition) {
						// Remove quotes if present
						if strings.HasPrefix(result, "'") && strings.HasSuffix(result, "'") {
							result = result[1 : len(result)-1]
						}
						return ep.processUnifiedParameter(result, masterDTO, serviceNameMap)
					}
				}
			}
		}
	}

	// Return default value (last rule that doesn't contain ->)
	if len(rules) > 0 {
		defaultValue := rules[len(rules)-1]
		// If the last rule contains ->, it's not a default value
		if !strings.Contains(defaultValue, "->") {
			if strings.HasPrefix(defaultValue, "'") && strings.HasSuffix(defaultValue, "'") {
				defaultValue = defaultValue[1 : len(defaultValue)-1]
			}
			return ep.processUnifiedParameter(defaultValue, masterDTO, serviceNameMap)
		}
	}

	return ""
}

// evaluateNameSplittingRules handles full name parsing
// Example: NAME:SPLIT:PART:first|middle|last
func (ep *ExpressionProcessor) evaluateNameSplittingRules(inputValue, ruleDefinition string, _ *common_dto.MasterDTO, _ map[string]*models.EsaLog) string {
	parts := strings.Split(ruleDefinition, ":")
	if len(parts) < 4 {
		return ""
	}

	// Extract which part to return
	partType := strings.ToLower(strings.TrimSpace(parts[3]))

	// Split the name using the business logic
	firstName, middleName, lastName := ep.splitFullName(inputValue)

	switch partType {
	case "first", "firstname":
		return firstName
	case "middle", "middlename":
		if middleName == "" {
			return "null"
		}
		return middleName
	case "last", "lastname":
		return lastName
	default:
		return inputValue
	}
}

// evaluateDateFormattingRules handles date format conversion
// Example: DATE:FORMAT:MMDDYYYY
func (ep *ExpressionProcessor) evaluateDateFormattingRules(inputValue, ruleDefinition string, _ *common_dto.MasterDTO, _ map[string]*models.EsaLog) string {
	parts := strings.Split(ruleDefinition, ":")
	if len(parts) < 3 {
		return inputValue
	}

	targetFormat := strings.TrimSpace(parts[2])

	parsedTime, ok := parseStandardDateTime(inputValue)
	if !ok {
		return inputValue // Return original if can't parse
	}

	// Convert target format to Go time format
	goFormat := ep.convertToGoTimeFormat(targetFormat)
	return parsedTime.Format(goFormat)
}

// evaluateStateMappingRules handles state code resolution with fallback
// Example: STATE:BUREAU:CIBIL:FALLBACK:<field1>||<field2>
func (ep *ExpressionProcessor) evaluateStateMappingRules(inputValue, ruleDefinition string, masterDTO *common_dto.MasterDTO, serviceNameMap map[string]*models.EsaLog) string {
	parts := strings.Split(ruleDefinition, ":")
	if len(parts) < 3 {
		return ""
	}

	bureauType := strings.ToUpper(strings.TrimSpace(parts[2]))

	// Handle fallback if specified
	if len(parts) >= 5 && strings.ToUpper(parts[3]) == "FALLBACK" {
		fallbackExpression := parts[4]
		inputValue = ep.evaluateUnifiedFallback(inputValue+"||"+fallbackExpression, masterDTO, serviceNameMap)
	}

	// Use existing state code resolution
	return ep.customResolveBureauStateCode([]string{inputValue, bureauType})
}

// evaluateGenericRules handles generic conditional logic
func (ep *ExpressionProcessor) evaluateGenericRules(inputValue, ruleDefinition string, masterDTO *common_dto.MasterDTO, serviceNameMap map[string]*models.EsaLog) string {
	// Parse generic rules format: condition1→result1:condition2→result2:default
	rules := strings.Split(ruleDefinition, ":")

	for _, rule := range rules {
		if strings.Contains(rule, "→") {
			parts := strings.Split(rule, "→")
			if len(parts) == 2 {
				condition := strings.TrimSpace(parts[0])
				result := strings.TrimSpace(parts[1])

				// Evaluate condition
				if ep.evaluateGenericCondition(condition, inputValue, masterDTO, serviceNameMap) {
					// Remove quotes if present
					if strings.HasPrefix(result, "'") && strings.HasSuffix(result, "'") {
						result = result[1 : len(result)-1]
					}
					return ep.processUnifiedParameter(result, masterDTO, serviceNameMap)
				}
			}
		}
	}

	// Return last rule as default if no → found
	if len(rules) > 0 {
		defaultValue := rules[len(rules)-1]
		if !strings.Contains(defaultValue, "→") {
			if strings.HasPrefix(defaultValue, "'") && strings.HasSuffix(defaultValue, "'") {
				defaultValue = defaultValue[1 : len(defaultValue)-1]
			}
			return ep.processUnifiedParameter(defaultValue, masterDTO, serviceNameMap)
		}
	}

	return ""
}

// evaluateConditionalRule evaluates a single conditional rule
func (ep *ExpressionProcessor) evaluateConditionalRule(rule string, inputValue string, masterDTO *common_dto.MasterDTO, serviceNameMap map[string]*models.EsaLog) bool {
	// Support both → and -> for keyboard accessibility
	separator := ""
	if strings.Contains(rule, "→") {
		separator = "→"
	} else if strings.Contains(rule, "->") {
		separator = "->"
	} else {
		return false
	}

	parts := strings.Split(rule, separator)
	if len(parts) != 2 {
		return false
	}

	condition := strings.TrimSpace(parts[0])
	return ep.evaluateGenericCondition(condition, inputValue, masterDTO, serviceNameMap)
}

// evaluateGenericCondition evaluates a generic condition against input value
func (ep *ExpressionProcessor) evaluateGenericCondition(condition, inputValue string, masterDTO *common_dto.MasterDTO, serviceNameMap map[string]*models.EsaLog) bool {
	// Handle different condition types
	switch {
	case strings.Contains(condition, "=="):
		parts := strings.Split(condition, "==")
		if len(parts) == 2 {
			left := ep.processUnifiedParameter(strings.TrimSpace(parts[0]), masterDTO, serviceNameMap)
			right := ep.processUnifiedParameter(strings.TrimSpace(parts[1]), masterDTO, serviceNameMap)
			return left == right
		}
	case strings.Contains(condition, "!="):
		parts := strings.Split(condition, "!=")
		if len(parts) == 2 {
			left := ep.processUnifiedParameter(strings.TrimSpace(parts[0]), masterDTO, serviceNameMap)
			right := ep.processUnifiedParameter(strings.TrimSpace(parts[1]), masterDTO, serviceNameMap)
			return left != right
		}
	case strings.Contains(condition, "LENGTH"):
		// Handle LENGTH == 10, LENGTH > 5, etc.
		if strings.Contains(condition, "==") {
			parts := strings.Split(condition, "==")
			if len(parts) == 2 && strings.TrimSpace(parts[0]) == "LENGTH" {
				expectedLength, err := strconv.Atoi(strings.TrimSpace(parts[1]))
				if err == nil {
					return len(inputValue) == expectedLength
				}
			}
		}
	case strings.Contains(condition, "IN"):
		// Handle IN ['M','Male','m'] syntax
		start := strings.Index(condition, "[")
		end := strings.LastIndex(condition, "]")
		if start != -1 && end != -1 && end > start {
			listStr := condition[start+1 : end]
			items := strings.Split(listStr, ",")
			for _, item := range items {
				item = strings.TrimSpace(item)
				if strings.HasPrefix(item, "'") && strings.HasSuffix(item, "'") {
					item = item[1 : len(item)-1]
				}
				if strings.EqualFold(inputValue, item) {
					return true
				}
			}
		}
		return false
	default:
		// Direct comparison
		conditionValue := ep.processUnifiedParameter(condition, masterDTO, serviceNameMap)
		return strings.EqualFold(inputValue, conditionValue)
	}

	return false
}

// extractResultFromRule extracts the result part from a conditional rule
func (ep *ExpressionProcessor) extractResultFromRule(rule string, masterDTO *common_dto.MasterDTO, serviceNameMap map[string]*models.EsaLog) string {
	// Support both → and -> for keyboard accessibility
	separator := ""
	if strings.Contains(rule, "→") {
		separator = "→"
	} else if strings.Contains(rule, "->") {
		separator = "->"
	} else {
		return ""
	}

	parts := strings.Split(rule, separator)
	if len(parts) != 2 {
		return ""
	}

	result := strings.TrimSpace(parts[1])
	// Remove quotes if present
	if strings.HasPrefix(result, "'") && strings.HasSuffix(result, "'") {
		result = result[1 : len(result)-1]
	}

	return ep.processUnifiedParameter(result, masterDTO, serviceNameMap)
}

// splitFullName implements the business logic for name splitting
func (ep *ExpressionProcessor) splitFullName(fullName string) (string, string, string) {
	// Trim and split on whitespace
	fullName = strings.TrimSpace(fullName)
	if fullName == "" {
		return "", "", ""
	}

	parts := strings.Fields(fullName)
	partCount := len(parts)

	switch partCount {
	case 1:
		return parts[0], "", "."
	case 2:
		return parts[0], "", parts[1]
	case 3:
		return parts[0], parts[1], parts[2]
	case 4:
		return parts[0] + " " + parts[1], parts[2], parts[3]
	case 5:
		return parts[0] + " " + parts[1], parts[2], parts[3] + " " + parts[4]
	case 6:
		return parts[0] + " " + parts[1], parts[2] + " " + parts[3], parts[4] + " " + parts[5]
	case 7:
		return parts[0] + " " + parts[1] + " " + parts[2], parts[3] + " " + parts[4], parts[5] + " " + parts[6]
	case 8:
		return parts[0] + " " + parts[1] + " " + parts[2], parts[3] + " " + parts[4] + " " + parts[5], parts[6] + " " + parts[7]
	case 9:
		return parts[0] + " " + parts[1] + " " + parts[2], parts[3] + " " + parts[4] + " " + parts[5], parts[6] + " " + parts[7] + " " + parts[8]
	default: // 10 or more parts - same as 9 parts rule
		return parts[0] + " " + parts[1] + " " + parts[2], parts[3] + " " + parts[4] + " " + parts[5], parts[6] + " " + parts[7] + " " + parts[8]
	}
}

// convertToGoTimeFormat converts common date formats to Go time format
func (ep *ExpressionProcessor) convertToGoTimeFormat(format string) string {
	// Convert common formats to Go format
	goFormat := format
	goFormat = strings.ReplaceAll(goFormat, "YYYY", "2006")
	goFormat = strings.ReplaceAll(goFormat, "MM", "01")
	goFormat = strings.ReplaceAll(goFormat, "DD", "02")
	goFormat = strings.ReplaceAll(goFormat, "MMDDYYYY", "01022006")
	goFormat = strings.ReplaceAll(goFormat, "DDMMYYYY", "02012006")
	goFormat = strings.ReplaceAll(goFormat, "YYYY-MM-DD", "2006-01-02")
	goFormat = strings.ReplaceAll(goFormat, "DD-MM-YYYY", "02-01-2006")
	goFormat = strings.ReplaceAll(goFormat, "MM/DD/YYYY", "01/02/2006")
	goFormat = strings.ReplaceAll(goFormat, "DD/MM/YYYY", "02/01/2006")

	return goFormat
}

// ============================================================================
// UNIFIED PARAMETER PROCESSING
// ============================================================================

// processUnifiedParameter handles all parameter types consistently across expression types
// unescapeSingleQuoteWrapped reverses the escaping applied by the single-quote wrap in
// processExpressionPlaceholders. When an expression result containing ':' is substituted back
// into a larger expression, it is wrapped as '...' with "\" -> "\\" and "'" -> "\'" so the
// re-parse does not split on inner colons or end the quote early. This reverses that
// ("\\" -> "\", "\'" -> "'") so the original value (e.g. JSON containing apostrophes such as a
// name like B'JOHN') is restored before downstream use (getJsonPath's json.Unmarshal, etc.).
// The \uE000 (private-use) placeholder ensures a literal "\\'" restores the backslash first so
// the following quote is not turned back into a delimiter.
func unescapeSingleQuoteWrapped(inner string) string {
	const quoteEscPlaceholder = "\uE000"
	inner = strings.ReplaceAll(inner, "\\\\", quoteEscPlaceholder)
	inner = strings.ReplaceAll(inner, "\\'", "'")
	inner = strings.ReplaceAll(inner, quoteEscPlaceholder, "\\")
	return inner
}

// This is the foundation for unified fallback support across ALL expression types
func (ep *ExpressionProcessor) processUnifiedParameter(param string, masterDTO *common_dto.MasterDTO, serviceNameMap map[string]*models.EsaLog) string {
	param = strings.TrimSpace(param)

	// Handle literal strings (quoted)
	if (strings.HasPrefix(param, "'") && strings.HasSuffix(param, "'")) ||
		(strings.HasPrefix(param, "\"") && strings.HasSuffix(param, "\"")) {
		inner := param[1 : len(param)-1]
		// Unescape if single-quoted (we escape when substituting expression results that contain ':')
		if param[0] == '\'' {
			inner = unescapeSingleQuoteWrapped(inner)
		}
		return inner
	}

	// Handle nested expressions FIRST (before fallback parsing)
	// Check for nested expressions more robustly (handle whitespace)
	trimmedParam := strings.TrimSpace(param)
	if strings.HasPrefix(trimmedParam, "{{") && strings.HasSuffix(trimmedParam, "}}") {
		inner := trimmedParam[2 : len(trimmedParam)-2]
		result := ep.evaluateExpression(inner, masterDTO, serviceNameMap)
		return result
	}

	// Handle fallback chains - CRITICAL: This enables || support across ALL expression types
	if strings.Contains(param, "||") && !ep.isWithinQuotes(param, strings.Index(param, "||")) {
		return ep.evaluateUnifiedFallback(param, masterDTO, serviceNameMap)
	}

	// Handle placeholders
	if strings.HasPrefix(param, "<") {
		return ep.getFieldValueFromPath(param, masterDTO, serviceNameMap)
	}
	// ((Service.field)): use pipeline substitution when direct lookup fails (e.g. nested CUSTOM/TRANSFORM)
	if strings.HasPrefix(param, "((") && strings.HasSuffix(param, "))") {
		direct := ep.getFieldValueFromPath(param, masterDTO, serviceNameMap)
		if debugAnalyticsAA && strings.Contains(param, "AACalculation") {
			fmt.Printf("[DEBUG processUnifiedParameter] param=%q getFieldValueFromPath directLen=%d empty=%v\n",
				param, len(direct), direct == "")
		}
		if direct != "" {
			return direct
		}
		resolved := ep.processServiceResponsePlaceholders(param, serviceNameMap, nil)
		if resolved != "" && resolved != param {
			return resolved
		}
		return direct
	}

	// Return as literal value
	return param
}

// evaluateUnifiedFallback processes fallback chains consistently across all expression types
func (ep *ExpressionProcessor) evaluateUnifiedFallback(expression string, masterDTO *common_dto.MasterDTO, serviceNameMap map[string]*models.EsaLog) string {
	// Split by || but respect nested expressions and quotes
	parts := ep.splitFallbackExpression(expression)

	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}

		// Process each fallback option directly to avoid circular dependency
		// Use getFieldValueFromPath directly for placeholders, avoiding processUnifiedParameter recursion
		var result string
		trimmed := strings.TrimSpace(part)
		// Nested {{...}} first: fallback arms must be evaluated as expressions before any
		// placeholder-only heuristics. DB/service paths require both delimiters (<...>, ((...))).
		if strings.HasPrefix(trimmed, "{{") && strings.HasSuffix(trimmed, "}}") {
			result = ep.processUnifiedParameter(part, masterDTO, serviceNameMap)
		} else if strings.HasPrefix(trimmed, "<") && strings.HasSuffix(trimmed, ">") {
			result = ep.getFieldValueFromPath(part, masterDTO, serviceNameMap)
		} else if strings.HasPrefix(trimmed, "((") && strings.HasSuffix(trimmed, "))") {
			result = ep.getFieldValueFromPath(part, masterDTO, serviceNameMap)
		} else if ep.isExpressionType(trimmed) {
			// Handle nested expression types in fallback (e.g., NUMERIC:0, CUSTOM:func:param)
			// Wrap in {{}} and process as a full placeholder to get proper evaluation
			wrappedExpression := "{{" + trimmed + "}}"
			result = ep.ProcessPlaceholders(wrappedExpression, masterDTO, serviceNameMap, nil, nil)
		} else {
			// For literal parts, use processUnifiedParameter (but these shouldn't contain ||)
			result = ep.processUnifiedParameter(part, masterDTO, serviceNameMap)
		}

		// Return first non-empty result
		if result != "" {
			return result
		}
	}

	// All fallback options were empty
	return ""
}

// isExpressionType checks if a string is an expression type (CALC:, NUMERIC:, etc.)
func (ep *ExpressionProcessor) isExpressionType(part string) bool {
	expressionTypes := []string{
		"CALC:", "FORMAT:", "TRANSFORM:", "CONCAT:", "CONDITION:",
		"CUSTOM:", "NUMERIC:", "BOOLEAN:", "JSON:", "ARRAY:", "CONDITIONAL:",
	}

	for _, exprType := range expressionTypes {
		if strings.HasPrefix(part, exprType) {
			return true
		}
	}
	return false
}

// splitFallbackExpression splits on || while respecting nested expressions and quotes
func (ep *ExpressionProcessor) splitFallbackExpression(expression string) []string {
	var parts []string
	var current strings.Builder
	var depth int
	var inQuotes bool
	var quoteChar rune

	// Use traditional for loop to properly handle i++ for skipping characters
	for i := 0; i < len(expression); i++ {
		char := rune(expression[i])
		switch char {
		case '\\':
			current.WriteRune(char)
			if inQuotes && i+1 < len(expression) {
				i++
				current.WriteRune(rune(expression[i])) // keep escaped char (\' / \\) verbatim
			}
		case '"', '\'':
			if !inQuotes {
				inQuotes = true
				quoteChar = char
			} else if char == quoteChar {
				inQuotes = false
			}
			current.WriteRune(char)
		case '{':
			if !inQuotes && i < len(expression)-1 && rune(expression[i+1]) == '{' {
				depth++
				current.WriteString("{{")
				i++ // Skip next character - this works in traditional for loop
				continue
			}
			current.WriteRune(char)
		case '}':
			if !inQuotes && i < len(expression)-1 && rune(expression[i+1]) == '}' {
				depth--
				current.WriteString("}}")
				i++ // Skip next character - this works in traditional for loop
				continue
			}
			current.WriteRune(char)
		case '|':
			if !inQuotes && depth == 0 && i < len(expression)-1 && rune(expression[i+1]) == '|' {
				// Found || at top level - split here
				parts = append(parts, current.String())
				current.Reset()
				i++ // Skip next | - this now works correctly
				continue
			}
			current.WriteRune(char)
		default:
			current.WriteRune(char)
		}
	}

	// Add final part
	if current.Len() > 0 {
		parts = append(parts, current.String())
	}

	return parts
}

// isWithinQuotes checks if a position is within quoted strings
func (ep *ExpressionProcessor) isWithinQuotes(expression string, position int) bool {
	var inQuotes bool
	var quoteChar rune
	var escapeNext bool

	for i, char := range expression {
		if i >= position {
			return inQuotes
		}
		if escapeNext {
			escapeNext = false
			continue
		}

		switch char {
		case '\\':
			if inQuotes {
				escapeNext = true // skip next char (\' / \\) inside quotes
			}
		case '"', '\'':
			if !inQuotes {
				inQuotes = true
				quoteChar = char
			} else if char == quoteChar {
				inQuotes = false
			}
		}
	}

	return inQuotes
}

// splitCommaSeparatedSources splits on comma while respecting nested expressions and quotes
// This is used for ARRAY merge operations to properly handle nested ARRAY expressions
func (ep *ExpressionProcessor) splitCommaSeparatedSources(expression string) []string {
	// Early exit: if no comma, return single element
	if !strings.Contains(expression, ",") {
		return []string{expression}
	}

	// Pre-allocate with reasonable capacity (most expressions have 2-3 sources)
	parts := make([]string, 0, 2)
	var current strings.Builder
	current.Grow(len(expression) / 2) // Pre-allocate builder capacity

	var braceDepth int   // Tracks {{...}} depth
	var bracketDepth int // Tracks [...] depth (for JSON arrays)
	var inQuotes bool
	var quoteChar byte

	// Work with bytes for better performance (expressions are typically ASCII)
	exprBytes := []byte(expression)

	for i := 0; i < len(exprBytes); i++ {
		char := exprBytes[i]
		switch char {
		case '"', '\'':
			if !inQuotes {
				inQuotes = true
				quoteChar = char
			} else if char == quoteChar {
				inQuotes = false
			}
			current.WriteByte(char)
		case '{':
			if !inQuotes {
				if i < len(exprBytes)-1 && exprBytes[i+1] == '{' {
					// Double brace {{ - increment brace depth
					braceDepth++
					current.WriteString("{{")
					i++ // Skip next character
					continue
				} else {
					// Single brace { - increment bracket depth (could be JSON object)
					bracketDepth++
				}
			}
			current.WriteByte(char)
		case '}':
			if !inQuotes {
				if i < len(exprBytes)-1 && exprBytes[i+1] == '}' {
					// Double brace }} - decrement brace depth
					braceDepth--
					current.WriteString("}}")
					i++ // Skip next character
					continue
				} else {
					// Single brace } - decrement bracket depth
					bracketDepth--
				}
			}
			current.WriteByte(char)
		case '[':
			if !inQuotes {
				bracketDepth++ // JSON array start
			}
			current.WriteByte(char)
		case ']':
			if !inQuotes {
				bracketDepth-- // JSON array end
			}
			current.WriteByte(char)
		case '\\':
			current.WriteByte(char)
			if inQuotes && i+1 < len(exprBytes) {
				i++
				current.WriteByte(exprBytes[i]) // keep escaped char (\' / \\) verbatim
			}
		case ',':
			// Only split on comma if we're at top level (no nested braces or brackets)
			if !inQuotes && braceDepth == 0 && bracketDepth == 0 {
				// Found comma at top level - split here
				parts = append(parts, current.String())
				current.Reset()
				continue
			}
			current.WriteByte(char)
		default:
			current.WriteByte(char)
		}
	}

	// Add final part
	if current.Len() > 0 {
		parts = append(parts, current.String())
	}

	return parts
}

// ============================================================================
// HELPER METHODS
// ============================================================================

// evaluateExpressionFallback handles fallback within expressions
func (ep *ExpressionProcessor) evaluateExpressionFallback(expression string, masterDTO *common_dto.MasterDTO, serviceNameMap map[string]*models.EsaLog) string {
	// Item 3: use the quote/brace-aware splitter (not the plain \|\| regex) so a literal "||"
	// inside a quoted value or a nested {{...}} is not mistaken for a fallback separator.
	parts := ep.splitFallbackExpression(expression)

	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}

		// Try to resolve this part
		var value string
		if strings.HasPrefix(part, "((") && strings.HasSuffix(part, "))") {
			// Service response placeholder
			placeholder := part[2 : len(part)-2]
			value = ep.getServiceResponseValue(placeholder, serviceNameMap, nil)
		} else if strings.HasPrefix(part, "<") && strings.HasSuffix(part, ">") {
			// Database placeholder
			placeholder := part[1 : len(part)-1]
			value = ep.getDbFieldValue(placeholder, masterDTO)
		} else {
			// Literal value or field reference - handle quoted literals
			if strings.HasPrefix(part, "'") && strings.HasSuffix(part, "'") {
				// Item 4: single-quoted literal — reverse the wrap escaping (\' -> ', \\ -> \) so a
				// value carrying apostrophes stays intact, consistent with the other unwrap sites.
				value = unescapeSingleQuoteWrapped(part[1 : len(part)-1])
			} else if strings.HasPrefix(part, "\"") && strings.HasSuffix(part, "\"") {
				value = part[1 : len(part)-1]
			} else {
				value = part
			}
		}

		if value != "" {
			return value
		}
	}

	return ""
}

// evaluateSimpleExpression handles simple field references
func (ep *ExpressionProcessor) evaluateSimpleExpression(expression string, masterDTO *common_dto.MasterDTO, serviceNameMap map[string]*models.EsaLog) string {
	return ep.getFieldValueFromPath(expression, masterDTO, serviceNameMap)
}

// getFieldValueFromPath gets field value from various sources
func (ep *ExpressionProcessor) getFieldValueFromPath(fieldPath string, masterDTO *common_dto.MasterDTO, serviceNameMap map[string]*models.EsaLog) string {
	fieldPath = strings.TrimSpace(fieldPath)

	// Handle service response placeholders
	if strings.HasPrefix(fieldPath, "((") && strings.HasSuffix(fieldPath, "))") {
		placeholder := fieldPath[2 : len(fieldPath)-2]
		return ep.getServiceResponseValue(placeholder, serviceNameMap, nil)
	}

	// Handle database placeholders
	if strings.HasPrefix(fieldPath, "<") && strings.HasSuffix(fieldPath, ">") {
		placeholder := fieldPath[1 : len(fieldPath)-1]
		return ep.getDbFieldValue(placeholder, masterDTO)
	}

	// Return as literal value
	return fieldPath
}

// getNumericValue converts field value to numeric
func (ep *ExpressionProcessor) getNumericValue(fieldPath string, masterDTO *common_dto.MasterDTO, serviceNameMap map[string]*models.EsaLog) float64 {
	// Use unified parameter processing for consistent fallback and nested expression support
	value := ep.processUnifiedParameter(fieldPath, masterDTO, serviceNameMap)
	if val, err := strconv.ParseFloat(value, 64); err == nil {
		return val
	}
	return 0
}

// ============================================================================
// DATABASE AND SERVICE RESPONSE PROCESSING (keeping existing implementations)
// ============================================================================

// processFallbackChainsAtTextLevel handles fallback chains at text level
func (ep *ExpressionProcessor) processFallbackChainsAtTextLevel(text string, masterDTO *common_dto.MasterDTO) string {
	if !strings.Contains(text, "||") {
		return text
	}

	// Split by || while respecting quotes and nested {{ }} segments
	parts := ep.splitFallbackExpression(text)
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}

		// Resolve DB placeholders (if any). For ARRAY/JSON/NUMERIC expressions we want to
		// preserve internal markers here so typed mode can still see __ARRAY_PARSE__/__JSON_PARSE__/__NUMERIC_INT__.
		resolvedRaw := strings.TrimSpace(ep.processDbDataPlaceholders(part, masterDTO))
		normalizedResolved := resolvedRaw
		if len(normalizedResolved) >= 2 && normalizedResolved[0] == '\'' && normalizedResolved[len(normalizedResolved)-1] == '\'' {
			normalizedResolved = unescapeSingleQuoteWrapped(normalizedResolved[1 : len(normalizedResolved)-1])
			normalizedResolved = strings.TrimSpace(normalizedResolved)
		}

		// Treat only plain unquoted empty / "null" as empty so configs can do "expr || []".
		// Keep quoted literals like 'null' as valid fallback values.
		if resolvedRaw == "" || resolvedRaw == "null" {
			continue
		}
		// Also treat explicit empty ARRAY/JSON markers as empty in text-level fallback chains.
		if normalizedResolved == "__ARRAY_PARSE__:null" || normalizedResolved == "__ARRAY_PARSE__:[]" {
			continue
		}

		// If the part contained DB placeholders but all resolved to empty (leaving only glue text),
		// treat it as empty so patterns like "Bearer <token1> || <token2>" behave as expected.
		withoutDbPlaceholders := strings.TrimSpace(ep.angleBracketRegex.ReplaceAllString(part, ""))
		hadDbPlaceholders := withoutDbPlaceholders != strings.TrimSpace(part)
		if hadDbPlaceholders && resolvedRaw == withoutDbPlaceholders {
			continue
		}

		return resolvedRaw
	}

	// If no part resolved, return empty string (lets callers keep defaults if needed)
	return ""
}

// processDbDataPlaceholders processes database data placeholders
func (ep *ExpressionProcessor) processDbDataPlaceholders(text string, masterDTO *common_dto.MasterDTO) string {
	// Note: No caching for database placeholders as data changes per request
	// Caching would lead to stale data being returned

	return ep.angleBracketRegex.ReplaceAllStringFunc(text, func(match string) string {
		placeholder := match[1 : len(match)-1] // Remove < and >

		// Always resolve fresh from masterDTO
		value := ep.getDbFieldValue(placeholder, masterDTO)

		return value
	})
}

// processServiceResponsePlaceholders processes service response placeholders.
// When itemContext is non-nil, ((item.fieldPath)) is resolved from it (for fan-out from array).
// When the placeholder is a bare service name (no path or ".response") and the value is non-empty
// JSON, we replace with "true" so that leftover {{CONDITION:...}} after step 5 can treat
// presence of a JSON body as truthy without embedding raw JSON. If the body is missing or empty,
// the placeholder is replaced with blank.
func (ep *ExpressionProcessor) processServiceResponsePlaceholders(text string, serviceNameMap map[string]*models.EsaLog, itemContext map[string]interface{}) string {
	return ep.doubleParenthesesRegex.ReplaceAllStringFunc(text, func(match string) string {
		placeholder := match[2 : len(match)-2] // Remove (( and ))
		value := ep.getServiceResponseValue(placeholder, serviceNameMap, itemContext)
		// Full body only (no path or ".response"): empty/missing → ""; JSON object/array → "true"; else raw value
		parts := strings.Split(strings.TrimSpace(placeholder), ".")
		isFullBody := len(parts) == 1 || (len(parts) == 2 && strings.TrimSpace(parts[1]) == "response")
		if debugPipeline {
			fmt.Printf("[SERVICE_PLACEHOLDER] match=%s placeholder=%s isFullBody=%v value=%s\n",
				match, placeholder, isFullBody, debugTruncate(value, 200))
		}
		if isFullBody {
			norm := ep.normalizeEmptyForComparison(value)
			if debugPipeline {
				fmt.Printf("[SERVICE_PLACEHOLDER] fullBody norm=%q → returning ", norm)
			}
			if norm == "" {
				if debugPipeline {
					fmt.Printf("blank\n")
				}
				return ""
			}
			if len(value) > 0 && (value[0] == '{' || value[0] == '[') {
				if debugPipeline {
					fmt.Printf("\"true\"\n")
				}
				return "true"
			}
			if debugPipeline {
				fmt.Printf("raw value (non-JSON fullBody)\n")
			}
		}
		return value
	})
}

// cleanupFallbackChainArtifacts removes remaining fallback chain artifacts.
// cleanupOpts may omit specific steps (see CleanupOmitRemoveDoublePipe, CleanupOmitTrimPipeSpaceEdges).
func (ep *ExpressionProcessor) cleanupFallbackChainArtifacts(text string, cleanupOpts *PlaceholderCleanupOptions) string {
	result := text

	if !cleanupOptsShouldOmit(cleanupOpts, CleanupOmitRemoveDoublePipe) {
		// Remove any remaining || operators that weren't processed
		result = strings.ReplaceAll(result, "||", "")
	}

	if !cleanupOptsShouldOmit(cleanupOpts, CleanupOmitTrimPipeSpaceEdges) {
		// Also remove single | characters that might be left over from partial processing
		// This handles cases where || got partially processed leaving | artifacts
		result = strings.TrimPrefix(result, "| ")
		result = strings.TrimSuffix(result, " |")
	}

	return result
}

// getDbFieldValue retrieves database field values from the masterDTO.
// This function implements case-insensitive object name matching while preserving
// case-insensitive field name matching in navigateNestedField.
//
// Case-Insensitive Object Names:
// - Object names are converted to lowercase for consistent lookup
// - This allows <Contact.age1__c> and <contact.age1__c> to both work
// - Field names are handled case-insensitively in navigateNestedField
//
// Examples:
// - <contact.age1__c> works (object: "contact", field: "age1__c" → "Age1__c")
// - <Contact.Age1__c> works (object: "contact", field: "Age1__c" → "Age1__c")
// - <LEAD.Id> works (object: "lead", field: "Id" → "Id")
//
// Parameters:
// - placeholder: The placeholder string (e.g., "contact.age1__c")
// - masterDTO: The master data transfer object containing all query results
//
// Returns:
// - String value of the field
// - Empty string if object or field not found
func (ep *ExpressionProcessor) getDbFieldValue(placeholder string, masterDTO *common_dto.MasterDTO) string {
	// Implementation from original file...
	if masterDTO == nil || masterDTO.Data == nil {
		return ""
	}

	// Handle conditional syntax: object[condition].field
	_, _, _, isConditional := utility.ParseConditionalPlaceholder(placeholder)
	if isConditional {
		return ep.handleConditionalDbField(placeholder, masterDTO)
	}

	// Handle simple field access: object.field
	parts := strings.Split(placeholder, ".")
	if len(parts) < 2 {
		return ""
	}

	objectName := strings.ToLower(strings.TrimSpace(parts[0]))

	// Navigate through nested fields
	if objectData, exists := masterDTO.Data[objectName]; exists {
		// First try direct navigation
		value := ep.navigateNestedField(objectData, parts[1:])
		if value != "" {
			return value
		}

		// If direct navigation failed and object is wrapped as {"records": [...]}, default to first record
		objMap := objectData
		if recordsVal, hasRecords := objMap["records"]; hasRecords {
			if records, ok := recordsVal.([]interface{}); ok && len(records) > 0 {
				value = ep.navigateNestedField(records[0], parts[1:])
				if value != "" {
					return value
				}
			}
		}

		return ""
	}

	return ""
}

// getValueFromItem resolves a path (e.g. "gstinId" or "data.x") from an item map (for fan-out ((item.xxx))).
func (ep *ExpressionProcessor) getValueFromItem(path string, item map[string]interface{}) string {
	if item == nil || path == "" {
		return ""
	}
	path = strings.TrimSpace(path)
	parts := strings.Split(path, ".")
	fieldParts := ep.splitPathWithArrayAccess(parts)
	raw := ep.navigateNestedFieldRaw(item, fieldParts)
	if raw == nil {
		return ""
	}
	switch v := raw.(type) {
	case string:
		return v
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	case int:
		return strconv.Itoa(v)
	case int64:
		return strconv.FormatInt(v, 10)
	case bool:
		if v {
			return "true"
		}
		return "false"
	default:
		if b, err := json.Marshal(raw); err == nil {
			return string(b)
		}
		return ""
	}
}

// getServiceResponseValue retrieves service response values.
// When itemContext is non-nil and placeholder starts with "item.", resolves from itemContext (fan-out).
func (ep *ExpressionProcessor) getServiceResponseValue(placeholder string, serviceNameMap map[string]*models.EsaLog, itemContext map[string]interface{}) string {
	placeholder = strings.TrimSpace(placeholder)
	if strings.HasPrefix(placeholder, "item.") && itemContext != nil {
		return ep.getValueFromItem(placeholder[5:], itemContext)
	}
	if serviceNameMap == nil {
		if debugAnalyticsAA && strings.Contains(placeholder, "AACalculation") {
			fmt.Printf("[DEBUG getServiceResponseValue] serviceNameMap is nil, placeholder=%q => empty\n", placeholder)
		}
		return ""
	}
	parts := strings.Split(placeholder, ".")
	if len(parts) < 1 {
		return ""
	}
	serviceName := strings.TrimSpace(parts[0])
	if debugAnalyticsAA && (serviceName == "AACalculation" || strings.Contains(placeholder, "Data__c")) {
		_, exists := serviceNameMap[serviceName]
		var bodyNil bool
		if log, ok := serviceNameMap[serviceName]; ok {
			bodyNil = log.Response.Body == nil
		}
		fmt.Printf("[DEBUG getServiceResponseValue] placeholder=%q serviceName=%q exists=%v bodyNil=%v mapLen=%d\n",
			placeholder, serviceName, exists, bodyNil, len(serviceNameMap))
	}

	// ENH-2: reserved status accessors. ((Service.__status)) and ((Service.__statusCode)) expose the
	// service's recorded execution status and HTTP status code as small scalars, so configs can gate
	// on a reliable signal instead of expanding the (potentially multi-MB) response body into a
	// condition or trusting a status code embedded in the body. They resolve against the current
	// resolution scope (services present in serviceNameMap); an absent service yields "" so
	// `((S.__status)) == 'COMPLETED'` is a safe affirmative "did it complete" gate.
	if len(parts) == 2 {
		switch strings.TrimSpace(parts[1]) {
		case reservedFieldStatus:
			if esaLog, exists := serviceNameMap[serviceName]; exists {
				return esaLog.Status
			}
			return ""
		case reservedFieldStatusCode:
			if esaLog, exists := serviceNameMap[serviceName]; exists {
				return strconv.Itoa(esaLog.Response.StatusCode)
			}
			return ""
		}
	}

	// Handle special cases for full response
	if len(parts) == 1 || (len(parts) == 2 && parts[1] == "response") {
		if esaLog, exists := serviceNameMap[serviceName]; exists {
			responseBody := esaLog.Response.Body

			if responseBody == nil {
				return ""
			}

			if jsonBytes, err := json.Marshal(responseBody); err == nil {
				return string(jsonBytes)
			}
		}
		return ""
	}

	// Resolved pre/post execution from ExtraFields (e.g. ((ServiceName.resolved_pre_execution.conditionChekLead)))
	if len(parts) >= 2 {
		if esaLog, exists := serviceNameMap[serviceName]; exists && esaLog.ExtraFields != nil {
			p1 := strings.TrimSpace(parts[1])
			if p1 == "resolved_pre_execution" || p1 == "resolved_post_execution" {
				data := esaLog.ExtraFields[p1]
				if data == nil {
					return ""
				}
				if len(parts) == 2 {
					if b, err := json.Marshal(data); err == nil {
						return string(b)
					}
					return ""
				}
				fieldParts := ep.splitPathWithArrayAccess(parts[2:])
				return ep.navigateNestedField(data, fieldParts)
			}
		}
	}

	// Handle nested field access from response body
	if esaLog, exists := serviceNameMap[serviceName]; exists {
		responseBody := esaLog.Response.Body

		if responseBody == nil {
			if debugAnalyticsAA && serviceName == "AACalculation" {
				fmt.Printf("[DEBUG getServiceResponseValue] AACalculation exists but Response.Body is nil => empty\n")
			}
			return ""
		}

		// Split the path properly to handle array access
		fieldParts := ep.splitPathWithArrayAccess(parts[1:])
		result := ep.navigateNestedField(responseBody, fieldParts)
		if debugAnalyticsAA && (serviceName == "AACalculation" || strings.Contains(placeholder, "Data__c")) {
			fmt.Printf("[DEBUG getServiceResponseValue] path=%q fieldParts=%v resultLen=%d empty=%v\n",
				placeholder, fieldParts, len(result), result == "")
		}
		return result
	}

	if debugAnalyticsAA && serviceName == "AACalculation" {
		fmt.Printf("[DEBUG getServiceResponseValue] serviceName %q not in map => empty\n", serviceName)
	}
	return ""
}

// splitPathWithArrayAccess properly splits a path to handle array access
func (ep *ExpressionProcessor) splitPathWithArrayAccess(parts []string) []string {
	var result []string

	for _, part := range parts {
		// Check if this part contains array access like field[0]
		if strings.Contains(part, "[") && strings.Contains(part, "]") {
			// Find the array access part
			indexStart := strings.Index(part, "[")
			indexEnd := strings.Index(part, "]")

			if indexStart < indexEnd {
				// Split into field name and array access
				fieldName := part[:indexStart]
				arrayAccess := part[indexStart : indexEnd+1]

				if fieldName != "" {
					result = append(result, fieldName)
				}
				result = append(result, arrayAccess)

				// Add remaining part after array access if any
				remaining := part[indexEnd+1:]
				if remaining != "" && strings.HasPrefix(remaining, ".") {
					remaining = remaining[1:] // Remove the leading dot
					if remaining != "" {
						result = append(result, remaining)
					}
				}
			} else {
				result = append(result, part)
			}
		} else {
			result = append(result, part)
		}
	}

	return result
}

// Reserved service field accessors (ENH-2). These are recognized on any ((Service.<field>))
// placeholder and take precedence over same-named response-body fields. Double-underscore prefix
// marks them as engine-reserved and avoids collisions with real bureau/API payload fields.
const (
	reservedFieldStatus     = "__status"     // -> esaLog.Status (e.g. "COMPLETED", "FAILED", "SKIPPED")
	reservedFieldStatusCode = "__statusCode" // -> esaLog.Response.StatusCode (HTTP status, as string)
)

// getServiceResponseValueRaw retrieves service response values without string conversion
func (ep *ExpressionProcessor) getServiceResponseValueRaw(placeholder string, serviceNameMap map[string]*models.EsaLog) interface{} {
	if serviceNameMap == nil {
		return nil
	}

	parts := strings.Split(placeholder, ".")
	if len(parts) < 1 {
		return nil
	}

	serviceName := strings.TrimSpace(parts[0])

	// ENH-2: reserved status accessors (see getServiceResponseValue). Returns the recorded status
	// string / numeric HTTP status code for services present in the current resolution scope.
	if len(parts) == 2 {
		switch strings.TrimSpace(parts[1]) {
		case reservedFieldStatus:
			if esaLog, exists := serviceNameMap[serviceName]; exists {
				return esaLog.Status
			}
			return nil
		case reservedFieldStatusCode:
			if esaLog, exists := serviceNameMap[serviceName]; exists {
				return esaLog.Response.StatusCode
			}
			return nil
		}
	}

	// Handle special cases for full response
	if len(parts) == 1 || (len(parts) == 2 && parts[1] == "response") {
		if esaLog, exists := serviceNameMap[serviceName]; exists {
			return esaLog.Response.Body
		}
		return nil
	}

	// Handle nested field access
	if esaLog, exists := serviceNameMap[serviceName]; exists {
		responseBody := esaLog.Response.Body
		// Split the path properly to handle array access
		fieldParts := ep.splitPathWithArrayAccess(parts[1:])
		rawResult := ep.navigateNestedFieldRaw(responseBody, fieldParts)
		return rawResult
	}

	return nil
}

// pluckFieldFromArrayOfMaps returns a slice of values for the given field from each map in the slice.
// If any element is not a map or the field is missing, returns nil so callers can treat as "no value".
// Used for paths like ((Service.data.result.uan)) when result is [{"uan":"1"},{"uan":"2"}].
func (ep *ExpressionProcessor) pluckFieldFromArrayOfMaps(arr []interface{}, fieldName string) []interface{} {
	if len(arr) == 0 || fieldName == "" {
		return nil
	}
	fieldLower := strings.ToLower(strings.TrimSpace(fieldName))
	result := make([]interface{}, 0, len(arr))
	for _, item := range arr {
		obj, ok := item.(map[string]interface{})
		if !ok {
			return nil
		}
		var v interface{}
		found := false
		for k, val := range obj {
			if strings.ToLower(strings.TrimSpace(k)) == fieldLower {
				v = val
				found = true
				break
			}
		}
		if !found {
			return nil
		}
		result = append(result, v)
	}
	return result
}

// navigateNestedFieldRaw navigates through nested object fields without string conversion
func (ep *ExpressionProcessor) navigateNestedFieldRaw(data interface{}, fieldParts []string) interface{} {
	current := data

	for _, part := range fieldParts {
		part = strings.TrimSpace(part)

		switch v := current.(type) {
		case map[string]interface{}:
			// First try exact match
			if value, exists := v[part]; exists {
				current = value
			} else {
				// If exact match fails, try case-insensitive match with trimmed keys
				// This handles keys with leading/trailing spaces (e.g., "MatchPercentage ")
				fieldNameLower := strings.ToLower(part)
				found := false
				for key, value := range v {
					trimmedKey := strings.TrimSpace(key)
					if strings.ToLower(trimmedKey) == fieldNameLower {
						current = value
						found = true
						break
					}
				}
				if !found {
					return nil
				}
			}
		case []interface{}:
			// Handle array access like field[0]
			if strings.Contains(part, "[") && strings.Contains(part, "]") {
				// Parse array index
				indexStart := strings.Index(part, "[")
				indexEnd := strings.Index(part, "]")
				if indexStart < indexEnd {
					indexStr := part[indexStart+1 : indexEnd]
					if index, err := strconv.Atoi(indexStr); err == nil && index >= 0 && index < len(v) {
						current = v[index]
						// Continue with remaining field path if any
						remaining := part[indexEnd+1:]
						if remaining != "" && strings.HasPrefix(remaining, ".") {
							fieldParts = append([]string{remaining[1:]}, fieldParts[1:]...)
						}
						continue
					}
				}
			}
			// Pluck: when path is "result.uan" and result is array of objects, return array of uan values
			// e.g. ((MobileUANService.data.result.uan)) with data.result = [{"uan":"1"},{"uan":"2"}] -> ["1","2"]
			plucked := ep.pluckFieldFromArrayOfMaps(v, part)
			if plucked != nil {
				current = plucked
			} else {
				return nil
			}
		default:
			// For non-traversable types, return nil if there are more parts
			if len(fieldParts) > 0 {
				return nil
			}
		}
	}

	return current
}

// convertToProperType converts string values to proper JSON types for typed mode
func (ep *ExpressionProcessor) convertToProperType(value string) interface{} {
	value = strings.TrimSpace(value)

	// If wrapped in single quotes (e.g. from leaf substitution in a re-pass or text-level
	// fallback), unquote first so internal markers like __ARRAY_PARSE__:/__JSON_PARSE__:/__NUMERIC_INT__:
	// can be recognized and converted to proper JSON types.
	if len(value) >= 2 && value[0] == '\'' && value[len(value)-1] == '\'' {
		inner := value[1 : len(value)-1]
		const quoteEscPlaceholder = "\uE000"
		inner = strings.ReplaceAll(inner, "\\\\", quoteEscPlaceholder)
		inner = strings.ReplaceAll(inner, "\\'", "'")
		inner = strings.ReplaceAll(inner, quoteEscPlaceholder, "\\")
		value = inner
	}

	// Handle special null markers for explicit type conversions
	switch value {
	case "__NULL_NUMERIC__":
		return nil // JSON null for numeric fields
	case "__NULL_BOOLEAN__":
		return nil // JSON null for boolean fields
	case "__BOOLEAN_TRUE__":
		return true // JSON boolean true
	case "__BOOLEAN_FALSE__":
		return false // JSON boolean false
	}

	// Handle numeric type markers
	if strings.HasPrefix(value, "__NUMERIC_INT__:") {
		intStr := value[len("__NUMERIC_INT__:"):]
		if intVal, err := strconv.Atoi(intStr); err == nil {
			return intVal
		}
	}
	if strings.HasPrefix(value, "__NUMERIC_FLOAT__:") {
		floatStr := value[len("__NUMERIC_FLOAT__:"):]
		if floatVal, err := strconv.ParseFloat(floatStr, 64); err == nil {
			return floatVal
		}
	}

	// Handle JSON parse marker (only from {{JSON:...}} expressions)
	if strings.HasPrefix(value, "__JSON_PARSE__:") {
		jsonStr := value[len("__JSON_PARSE__:"):]
		var jsonValue interface{}
		if err := json.Unmarshal([]byte(jsonStr), &jsonValue); err == nil {
			return jsonValue // Return the actual JSON object/array
		}
		// If parsing fails, return empty object
		return map[string]interface{}{}
	}

	// Handle ARRAY parse marker (only from {{ARRAY:...}} expressions)
	if strings.HasPrefix(value, "__ARRAY_PARSE__:") {
		arrayStr := value[len("__ARRAY_PARSE__:"):]
		var arrayValue []interface{}
		if err := json.Unmarshal([]byte(arrayStr), &arrayValue); err == nil {
			return arrayValue // Return the actual JSON array
		}
		// If parsing fails, return empty array
		return []interface{}{}
	}

	// For all other values, return as string - no automatic JSON conversion
	// Regular field access like ((Service.Field)) will return as strings
	return value
}

// ============================================================================
// CUSTOM FUNCTION IMPLEMENTATIONS
// ============================================================================

// customGenerateId generates unique ID with optional prefix
func (ep *ExpressionProcessor) customGenerateId(params []string) string {
	prefix := "id"
	if len(params) > 0 && params[0] != "" {
		prefix = params[0]
	}

	// Generate random suffix
	rand.Seed(time.Now().UnixNano())
	suffix := rand.Intn(999999)

	return fmt.Sprintf("%s_%06d", prefix, suffix)
}

// customCalculateAge calculates age from date of birth
func (ep *ExpressionProcessor) customCalculateAge(params []string) string {
	if len(params) < 1 || params[0] == "" {
		return "0"
	}

	dob, ok := parseStandardDateTime(params[0])
	if !ok {
		return "0"
	}

	now := time.Now()
	age := now.Year() - dob.Year()

	// Adjust if birthday hasn't occurred this year
	if now.YearDay() < dob.YearDay() {
		age--
	}

	return fmt.Sprintf("%d", age)
}

// customValidatePAN validates Indian PAN number format
func (ep *ExpressionProcessor) customValidatePAN(params []string) string {
	if len(params) < 1 || params[0] == "" {
		return "false"
	}

	pan := strings.TrimSpace(strings.ToUpper(params[0]))

	// PAN format: 5 letters + 4 digits + 1 letter
	if len(pan) != 10 {
		return "false"
	}

	// Check pattern: AAAAA9999A
	for i, char := range pan {
		if i < 5 || i == 9 {
			// Should be letter
			if !unicode.IsLetter(char) {
				return "false"
			}
		} else {
			// Should be digit
			if !unicode.IsDigit(char) {
				return "false"
			}
		}
	}

	return "true"
}

// customFormatPhone formats phone number
func (ep *ExpressionProcessor) customFormatPhone(params []string) string {
	if len(params) < 1 || params[0] == "" {
		return ""
	}

	phone := regexp.MustCompile(`[^\d]`).ReplaceAllString(params[0], "")

	if len(phone) == 10 {
		return fmt.Sprintf("+91-%s-%s", phone[:5], phone[5:])
	} else if len(phone) == 12 && strings.HasPrefix(phone, "91") {
		mobile := phone[2:]
		return fmt.Sprintf("+91-%s-%s", mobile[:5], mobile[5:])
	}

	return params[0] // Return original if can't format
}

// customGetCurrentTimestamp returns current timestamp with user-friendly format support
func (ep *ExpressionProcessor) customGetCurrentTimestamp(params []string) string {
	format := "2006-01-02T15:04:05Z"
	if len(params) > 0 && params[0] != "" {
		format = ep.translateDateFormat(params[0])
	}

	return time.Now().UTC().Format(format)
}

// translateDateFormat converts user-friendly date formats to Go time format
func (ep *ExpressionProcessor) translateDateFormat(userFormat string) string {
	switch strings.ToUpper(userFormat) {
	// Common date formats
	case "MMDDYYYY":
		return "01022006"
	case "DDMMYYYY":
		return "02012006"
	case "YYYYMMDD":
		return "20060102"
	case "YYYYMMDDHHMMSS":
		return "20060102150405"
	case "MM/DD/YYYY":
		return "01/02/2006"
	case "DD/MM/YYYY":
		return "02/01/2006"
	case "YYYY/MM/DD":
		return "2006/01/02"
	case "MM-DD-YYYY":
		return "01-02-2006"
	case "DD-MM-YYYY":
		return "02-01-2006"
	case "YYYY-MM-DD":
		return "2006-01-02"
	case "YYYY-MM-DDTHH:MM:SS":
		return "2006-01-02T15:04:05"
	case "YYYY-MM-DDTHH:MM:SSZ":
		return "2006-01-02T15:04:05Z"
	case "YYYY-MM-DDTHH:MM:SS.SSSZ":
		return "2006-01-02T15:04:05.000Z"
	case "YYYY-MM-DDTHH:MM:SSZZ:ZZ":
		return "2006-01-02T15:04:05-07:00"
	case "YYYY-MM-DDTHH:MM:SS.SSSZZ:ZZ":
		return "2006-01-02T15:04:05.000-07:00"
	case "YYYY-MM-DDTHH:MM:SSZZZZ":
		return "2006-01-02T15:04:05-0700"
	case "YYYY-MM-DDTHH:MM:SS.SSSZZZZ":
		return "2006-01-02T15:04:05.000-0700"
	case "YYYY-MM-DD HH:MM":
		return "2006-01-02 15:04"
	case "YYYY-MM-DD HH:MM:SS":
		return "2006-01-02 15:04:05"
	// Time formats
	case "HHMMSS":
		return "150405"
	case "HH:MM:SS":
		return "15:04:05"
	case "HHMM":
		return "1504"
	case "HH:MM":
		return "15:04"
	// DateTime formats
	case "MMDDYYYY_HHMMSS":
		return "01022006_150405"
	case "YYYYMMDD_HHMMSS":
		return "20060102_150405"
	case "MMDDYYYY HH:MM:SS":
		return "01022006 15:04:05"
	case "DD/MM/YYYY HH:MM:SS":
		return "02/01/2006 15:04:05"
		// Legacy shortcuts
	case "ISO8601_COMPACT":
		return "2006-01-02T15:04:05Z"
	case "TIMESTAMP":
		return "2006-01-02T15:04:05.000Z"
	case "ISO8601":
		return "2006-01-02T15:04:05.000Z"
	default:
		// Return as-is for Go time format strings
		return userFormat
	}
}

// customCleanSpecialChars removes special characters
func (ep *ExpressionProcessor) customCleanSpecialChars(params []string) string {
	if len(params) < 1 || params[0] == "" {
		return ""
	}

	// Keep only letters, digits, and spaces
	reg := regexp.MustCompile(`[^a-zA-Z0-9\s]`)
	return reg.ReplaceAllString(params[0], "")
}

// customTakeLast takes last N characters
func (ep *ExpressionProcessor) customTakeLast(params []string) string {
	if len(params) < 2 || params[0] == "" {
		return ""
	}

	text := params[0]
	count, err := strconv.Atoi(params[1])
	if err != nil || count <= 0 {
		return ""
	}

	if len(text) <= count {
		return text
	}

	return text[len(text)-count:]
}

// customSha256Hash calculates SHA-256 hash
func (ep *ExpressionProcessor) customSha256Hash(params []string) string {
	if len(params) < 1 || params[0] == "" {
		return ""
	}

	hash := sha256.New()
	hash.Write([]byte(params[0]))
	return hex.EncodeToString(hash.Sum(nil))
}

// customBase64Encode encodes the input as base64 (RFC 4648 std encoding)
func (ep *ExpressionProcessor) customBase64Encode(params []string) string {
	if len(params) < 1 {
		return ""
	}
	return base64.StdEncoding.EncodeToString([]byte(params[0]))
}

// customGetJsonPath parses a stringified JSON and returns the value at the given dot-separated path.
// Path supports object keys and array indices, e.g. "output.0" or "data.items.1.name".
// First param: JSON string from any source—SF/DB field (e.g. <Lead.String_response__c>) or service
// response (e.g. ((AACalculation.Data__c))). Both are resolved before parsing.
// Second param: path (e.g. "output" or "output.0" for first element of output array).
// For JSON that contains invalid NaN, wrap the first param in normalizeJsonNan (e.g. getJsonPath:normalizeJsonNan:((Service.field)):path).
// Returns __JSON_PARSE__:... for objects/arrays (for embedding in request body), or plain string for primitives.
func (ep *ExpressionProcessor) customGetJsonPath(params []string) string {
	if len(params) < 2 || params[0] == "" || params[1] == "" {
		return ""
	}
	jsonStr := strings.TrimSpace(params[0])
	pathStr := strings.TrimSpace(params[1])
	var root interface{}
	if err := json.Unmarshal([]byte(jsonStr), &root); err != nil {
		return ""
	}
	// Split path by dot; each segment can be a key, numeric index, or key with bracket notation.
	// Supported bracket forms:
	//   key[N]                → navigate to Nth element of the array at key
	//   key[field=='value']   → find first array element at key where field == value
	//   key[field=="value"]   → same, double-quoted value
	//   key[field==value]     → same, unquoted value
	segments := strings.Split(pathStr, ".")
	current := root
	for _, seg := range segments {
		seg = strings.TrimSpace(seg)
		if seg == "" {
			continue
		}
		if current == nil {
			return ""
		}
		// Bare numeric index (e.g. "0", "1") — navigate directly into current array
		if idx, err := strconv.Atoi(seg); err == nil {
			arr, ok := current.([]interface{})
			if !ok || idx < 0 || idx >= len(arr) {
				return ""
			}
			current = arr[idx]
			continue
		}
		// Object key, with optional bracket suffix: key, key[N], or key[predicate]
		// Predicate uses the same evaluateFilterCondition engine as ARRAY:filter, giving
		// full AND / OR / IN / NOT IN and comparison operator (==, !=, >, <, >=, <=) support.
		key := seg
		arrayIdx := -1
		filterPredicate := ""
		if bracket := strings.Index(seg, "["); bracket >= 0 && strings.HasSuffix(seg, "]") {
			key = strings.TrimSpace(seg[:bracket])
			bracketContent := strings.TrimSpace(seg[bracket+1 : len(seg)-1])
			if i, e := strconv.Atoi(bracketContent); e == nil {
				// Numeric index: key[N]
				arrayIdx = i
			} else {
				// Filter predicate: key[field==value], key[f1==v1 AND f2==v2],
				//                   key[f IN (a,b)], key[f==v1 OR f==v2], etc.
				filterPredicate = bracketContent
			}
		}
		obj, ok := current.(map[string]interface{})
		if !ok {
			return ""
		}
		var next interface{}
		found := false
		for k, v := range obj {
			if strings.EqualFold(strings.TrimSpace(k), key) {
				next = v
				found = true
				break
			}
		}
		if !found {
			return ""
		}
		current = next
		if arrayIdx >= 0 {
			// Numeric index
			arr, ok := current.([]interface{})
			if !ok || arrayIdx >= len(arr) {
				return ""
			}
			current = arr[arrayIdx]
		} else if filterPredicate != "" {
			// Predicate filter: delegate to evaluateFilterCondition (same engine as ARRAY:filter)
			arr, ok := current.([]interface{})
			if !ok {
				return ""
			}
			matched := false
			for _, elem := range arr {
				if elemMap, eok := elem.(map[string]interface{}); eok {
					if ep.evaluateFilterCondition(elemMap, filterPredicate) {
						current = elem
						matched = true
						break
					}
				}
			}
			if !matched {
				return ""
			}
		}
	}
	if current == nil {
		return ""
	}
	// Return with __JSON_PARSE__ for objects/arrays so request body embedding works
	switch v := current.(type) {
	case map[string]interface{}, []interface{}:
		bytes, err := json.Marshal(v)
		if err != nil {
			return ""
		}
		return fmt.Sprintf("__JSON_PARSE__:%s", string(bytes))
	default:
		return fmt.Sprintf("%v", v)
	}
}

// pincodeDigits is the fixed digit count of an Indian PIN code.
const pincodeDigits = 6

// customExtractPincode pulls a 6-digit PIN code out of free-text address strings.
//
// Syntax:
//   - {{CUSTOM:extractPincode:<addressText>}}
//   - {{CUSTOM:extractPincode:<addressText>:<fallbackSource>:...}}
//
// Each parameter is searched in order and the first PIN code found wins, so an explicit pincode
// field can be supplied as a later argument:
//
//	{{CUSTOM:extractPincode:<Contact.Office_Address__c>:<Contact.Office_Pincode__c>}}
//
// Within one parameter the search order ports the Apex fetchOfficePincode helper:
//
//  1. The last 6 characters of the trimmed text, when all digits — the common
//     "Plot 5, MG Road, Bengaluru 560001" shape.
//  2. The first comma-separated part that is exactly 6 digits after trimming — the
//     "Plot 5, MG Road, Bengaluru, 560001" shape.
//  3. The last standalone 6-digit token anywhere in the text — the
//     "Plot 5, Bengaluru 560001, Karnataka, India" shape.
//
// Steps 1 and 2 mirror the Apex exactly, so every address the Apex resolved resolves to the same
// value here. Step 3 only runs when the Apex would have returned null, so it can turn a blank
// into a value but never change a value the Apex already produced. It scans right to left because
// Indian addresses place the PIN code near the end, which stops a leading plot or flat number
// from winning.
//
// Returns "" when no PIN code is found; combine with `||` for a literal default.
func (ep *ExpressionProcessor) customExtractPincode(params []string) string {
	for _, param := range params {
		if pincode := extractPincodeFromText(param); pincode != "" {
			return pincode
		}
	}
	return ""
}

func extractPincodeFromText(text string) string {
	address := strings.TrimSpace(text)
	if len(address) < pincodeDigits {
		return ""
	}

	// 1. Trailing PIN code.
	if tail := address[len(address)-pincodeDigits:]; isAllASCIIDigits(tail) {
		return tail
	}

	// 2. A comma-separated part that is nothing but the PIN code.
	for _, part := range strings.Split(address, ",") {
		part = strings.TrimSpace(part)
		if len(part) == pincodeDigits && isAllASCIIDigits(part) {
			return part
		}
	}

	// 3. Last standalone 6-digit token, scanning right to left.
	tokens := strings.FieldsFunc(address, func(r rune) bool {
		return unicode.IsSpace(r) || r == ',' || r == ';' || r == '-'
	})
	for i := len(tokens) - 1; i >= 0; i-- {
		token := strings.Trim(tokens[i], ".")
		if len(token) == pincodeDigits && isAllASCIIDigits(token) {
			return token
		}
	}

	return ""
}

// isAllASCIIDigits reports whether s is non-empty and contains only '0'-'9'. It stands in for Apex
// String.isNumeric() for the PIN code case while rejecting non-ASCII digits, signs and decimals.
func isAllASCIIDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// customNormalizeJsonNan returns the input string with all "NaN" replaced by "0" so that
// stringified JSON from services (e.g. Analytics AA) can be parsed by getJsonPath. Use as a
// nested expression when the JSON source may contain NaN: getJsonPath:normalizeJsonNan:((Service.field)):path.
func (ep *ExpressionProcessor) customNormalizeJsonNan(params []string) string {
	if len(params) < 1 {
		return ""
	}
	return strings.ReplaceAll(strings.TrimSpace(params[0]), "NaN", "0")
}

// customJoinDistinctField extracts field values from an array of objects, optionally filters
// by field/value, removes duplicates while preserving first-seen order, and joins with delimiter.
//
// Syntax:
// - {{CUSTOM:joinDistinctField:<arraySource>:<fieldPath>}}
// - {{CUSTOM:joinDistinctField:<arraySource>:<fieldPath>:<delimiter>}}
// - {{CUSTOM:joinDistinctField:<arraySource>:<fieldPath>:<delimiter>:<filterFieldPath>:<filterValue>}}
//
// Examples:
// - {{CUSTOM:joinDistinctField:{{ARRAY:<multibureau_idlist__c>}}:Id_Value__c}}
// - {{CUSTOM:joinDistinctField:{{ARRAY:<multibureau_idlist__c>}}:Id_Value__c:, :Iden_Type__c:PAN}}
func (ep *ExpressionProcessor) customJoinDistinctField(params []string) string {
	if len(params) < 2 {
		return ""
	}

	source := strings.TrimSpace(params[0])
	targetFieldPath := strings.TrimSpace(params[1])
	if source == "" || targetFieldPath == "" {
		return ""
	}

	delimiter := ","
	if len(params) >= 3 && strings.TrimSpace(params[2]) != "" {
		delimiter = params[2]
	}

	filterFieldPath := ""
	filterValue := ""
	if len(params) >= 5 {
		filterFieldPath = strings.TrimSpace(params[3])
		filterValue = strings.TrimSpace(params[4])
	}

	sourceArray := ep.parseCustomArraySource(source)
	if len(sourceArray) == 0 {
		return ""
	}

	targetParts := ep.splitPathWithArrayAccess(strings.Split(targetFieldPath, "."))
	filterParts := []string{}
	if filterFieldPath != "" {
		filterParts = ep.splitPathWithArrayAccess(strings.Split(filterFieldPath, "."))
	}

	seen := make(map[string]struct{}, len(sourceArray))
	values := make([]string, 0, len(sourceArray))

	for _, item := range sourceArray {
		itemMap, ok := item.(map[string]interface{})
		if !ok {
			continue
		}

		if len(filterParts) > 0 {
			rawFilterValue := ep.navigateNestedFieldRaw(itemMap, filterParts)
			if !strings.EqualFold(strings.TrimSpace(ep.arrayElementToString(rawFilterValue)), filterValue) {
				continue
			}
		}

		rawValue := ep.navigateNestedFieldRaw(itemMap, targetParts)
		value := strings.TrimSpace(ep.arrayElementToString(rawValue))
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}

		seen[value] = struct{}{}
		values = append(values, value)
	}

	return strings.Join(values, delimiter)
}

// parseCustomArraySource resolves a custom function source into []interface{}.
// Supports __ARRAY_PARSE__, __JSON_PARSE__, plain JSON arrays, and JSON objects with a records array.
func (ep *ExpressionProcessor) parseCustomArraySource(source string) []interface{} {
	source = strings.TrimSpace(source)
	if source == "" {
		return []interface{}{}
	}

	raw := source
	switch {
	case strings.HasPrefix(raw, "__ARRAY_PARSE__:"):
		raw = strings.TrimSpace(strings.TrimPrefix(raw, "__ARRAY_PARSE__:"))
	case strings.HasPrefix(raw, "__JSON_PARSE__:"):
		raw = strings.TrimSpace(strings.TrimPrefix(raw, "__JSON_PARSE__:"))
	}

	var arrayValue []interface{}
	if err := json.Unmarshal([]byte(raw), &arrayValue); err == nil {
		return arrayValue
	}

	var objectValue map[string]interface{}
	if err := json.Unmarshal([]byte(raw), &objectValue); err == nil {
		if records, ok := objectValue["records"].([]interface{}); ok {
			return records
		}
	}

	return []interface{}{}
}

// customDateWithinDays checks if date is within specified days
func (ep *ExpressionProcessor) customDateWithinDays(params []string) string {
	if len(params) < 2 || params[0] == "" || params[1] == "" {
		return "false"
	}

	dateStr := params[0]
	days, err := strconv.Atoi(params[1])
	if err != nil {
		return "false"
	}

	date, ok := parseStandardDateTime(dateStr)
	if !ok {
		return "false"
	}

	daysSince := int(time.Since(date).Hours() / 24)
	if daysSince >= 0 && daysSince <= days {
		return "true"
	}
	return "false"
}

// parseDateAuto parses a date string when format is not specified. Uses a single-pass parser (dateparse)
// for time and memory efficiency: no loop over multiple layouts, minimal allocations via library pooling.
func parseDateAuto(dateStr string) (time.Time, bool) {
	t, err := dateparse.ParseAny(dateStr)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

var standardDateFormats = []string{
	"2006-01-02T15:04:05.000-0700", // Salesforce datetime with ±HHMM offset (e.g. .000+0000)
	"2006-01-02T15:04:05-0700",     // Datetime with ±HHMM offset without millis
	"2006-01-02T15:04:05.000Z",     // Salesforce datetime (Z suffix)
	"2006-01-02T15:04:05Z",         // ISO datetime (Z suffix)
	"2006-01-02T15:04:05-07:00",    // ISO datetime with ±HH:MM offset
	"2006-01-02T15:04:05",          // Basic datetime format
	"2006-01-02 15:04:05",          // MySQL datetime format
	"2006-01-02",                   // Date only format
	"01/02/2006",                   // Slash format (legacy precedence retained)
	"02/01/2006",                   // Slash format alternate
	"02-01-2006",                   // Dash format DD-MM-YYYY
	"01-02-2006",                   // Dash format alternate
}

// parseStandardDateTime keeps ESA's legacy explicit layouts as the first pass and then
// falls back to parseDateAuto for broader dateparse-based support. This keeps common
// behavior aligned across custom functions and formatting transforms.
func parseStandardDateTime(dateStr string) (time.Time, bool) {
	dateStr = strings.TrimSpace(dateStr)
	if dateStr == "" {
		return time.Time{}, false
	}

	for _, format := range standardDateFormats {
		if parsed, err := time.Parse(format, dateStr); err == nil {
			return parsed, true
		}
	}

	return parseDateAuto(dateStr)
}

// customDaysSince calculates days since given date.
// Single param: date string. When format is not specified, uses single-pass format detection (see docs).
func (ep *ExpressionProcessor) customDaysSince(params []string) string {
	if len(params) < 1 || params[0] == "" {
		return "0"
	}
	dateStr := strings.TrimSpace(params[0])
	date, ok := parseStandardDateTime(dateStr)
	if !ok {
		return "0"
	}
	daysSince := int(time.Since(date).Hours() / 24)
	return fmt.Sprintf("%d", daysSince)
}

// customMonthsSince returns whole months from the given date to today (vintage / work experience in months).
// Params: (dateString, dateFormat). dateFormat is optional; if omitted, single-pass format detection is used (see docs).
// When format is specified: use translateDateFormat names (DD/MM/YYYY, MM/DD/YYYY, YYYY-MM-DD, etc.).
// Formula: (now.year - date.year)*12 + (now.month - date.month). Reusable for UAN vintage, work experience, etc.
func (ep *ExpressionProcessor) customMonthsSince(params []string) string {
	if len(params) < 1 || params[0] == "" {
		return "0"
	}
	dateStr := strings.TrimSpace(params[0])
	var date time.Time
	var ok bool
	if len(params) >= 2 && strings.TrimSpace(params[1]) != "" {
		layout := ep.translateDateFormat(strings.TrimSpace(params[1]))
		var err error
		date, err = time.Parse(layout, dateStr)
		ok = (err == nil)
	} else {
		date, ok = parseStandardDateTime(dateStr)
	}
	if !ok {
		return "0"
	}
	now := time.Now()
	months := (now.Year()-date.Year())*12 + (int(now.Month()) - int(date.Month()))
	return fmt.Sprintf("%d", months)
}

// customGetNumericValue extracts numeric value from string
func (ep *ExpressionProcessor) customGetNumericValue(params []string) string {
	if len(params) < 1 || params[0] == "" {
		return "0"
	}

	// Extract digits from string
	reg := regexp.MustCompile(`[^\d.-]`)
	numStr := reg.ReplaceAllString(params[0], "")

	if val, err := strconv.ParseFloat(numStr, 64); err == nil {
		if val == float64(int(val)) {
			return fmt.Sprintf("%d", int(val))
		}
		return fmt.Sprintf("%g", val)
	}

	return "0"
}

// customCalculateFOIR calculates FOIR (Fixed Obligation to Income Ratio) based on Apex logic
func (ep *ExpressionProcessor) customCalculateFOIR(params []string) string {
	// Expected parameters in order:
	// 0: loanAmount (Lead.Amount_in_Rs__c)
	// 1: loanRate (Lead.Loan_Rate__c)
	// 2: loanTenor (Lead.Loan_Tenor_in_Month__c)
	// 3: declaredMonthlyIncome (Lead.Net_Salary_Monthly__c)
	// 4: proposedEMI (Lead.Proposed_EMI__c) - can be empty/null
	// 5: leadSource (Lead.LeadSource_S__r.Name)
	// 6: foirOnImputedIncome (Policy_Parameter__c.Foir_on_imputed_income__c) - "true"/"false"
	// 7: incomeModelPrediction (From API response)
	// 8: existingAccounts (JSON array of MultiBureau_AccountList__c records)
	// 9: creditCardIndebtedness (Re.ccIndebtedness)

	if len(params) < 10 {
		return "-1" // Invalid parameters
	}

	// Parse numeric parameters
	loanAmount, _ := strconv.ParseFloat(params[0], 64)
	loanRate, _ := strconv.ParseFloat(params[1], 64)
	loanTenor, _ := strconv.ParseFloat(params[2], 64)
	declaredMonthlyIncome, _ := strconv.ParseFloat(params[3], 64)
	proposedEMI, _ := strconv.ParseFloat(params[4], 64)
	leadSource := params[5]
	foirOnImputedIncome := params[6] == "true"
	incomeModelPrediction, _ := strconv.ParseFloat(params[7], 64)
	existingAccountsJSON := params[8]
	creditCardIndebtedness, _ := strconv.ParseFloat(params[9], 64)

	// Step 1: Determine Income to Use
	incomeToUse := ep.determineIncomeToUse(declaredMonthlyIncome, incomeModelPrediction)

	// Step 2: Calculate Average Salary
	averageSalary := ep.calculateAverageSalary(declaredMonthlyIncome, incomeToUse, foirOnImputedIncome, leadSource)

	// Step 3: Calculate Existing Obligations
	existingObligations := ep.calculateExistingObligations(existingAccountsJSON)

	// Step 4: Calculate Proposed EMI
	finalProposedEMI := ep.calculateProposedEMI(proposedEMI, loanAmount, loanRate, int(loanTenor), leadSource)

	// Step 5: Calculate FOIR
	foirValue := ep.calculateFOIRValue(existingObligations, finalProposedEMI, creditCardIndebtedness, averageSalary)

	return fmt.Sprintf("%.2f", foirValue)
}

// StateCode represents a state's bureau codes as compile-time constants
type StateCode struct {
	CIBIL string
	CRIF  string
}

// Static state lookup map - initialized at compile time for maximum performance
// No sync.Once needed since this is immutable static data
var stateToCodeMap = map[string]StateCode{
	// Pre-computed lookup table for all state variants (alphabetical by state name)

	// Andaman & Nicobar Islands (35, AN)
	"andaman & nicobar islands": {"35", "AN"}, "andaman and nicobar islands": {"35", "AN"}, "andaman & nicobar": {"35", "AN"}, "andaman and nicobar": {"35", "AN"}, "andaman & nicobar island": {"35", "AN"}, "andaman and nicobar island": {"35", "AN"}, "an": {"35", "AN"}, "a.n": {"35", "AN"}, "a.n.i": {"35", "AN"},

	// Andhra Pradesh (28, AP)
	// Note: "a.p" / "a.p." also appear in CIBIL variants for Arunachal Pradesh; mapped to Andhra Pradesh
	// to be consistent with CRIF where "AP" unambiguously denotes Andhra Pradesh.
	"andhra pradesh": {"28", "AP"}, "ap": {"28", "AP"}, "a.p": {"28", "AP"}, "a.p.": {"28", "AP"}, "andra": {"28", "AP"}, "andra pradesh": {"28", "AP"}, "andra prdesh": {"28", "AP"},

	// Arunachal Pradesh (12, AR)
	"arunachal pradesh": {"12", "AR"}, "arunachal prdesh": {"12", "AR"}, "anurachal pradesh": {"12", "AR"}, "anurachal prdesh": {"12", "AR"},

	// Assam (18, AS)
	"assam": {"18", "AS"}, "as": {"18", "AS"}, "a.s": {"18", "AS"}, "asam": {"18", "AS"},

	// Bihar (10, BR)
	"bihar": {"10", "BR"}, "br": {"10", "BR"},

	// Chandigarh (04, CH)
	"chandigarh": {"04", "CH"}, "c.h": {"04", "CH"}, "chandigar": {"04", "CH"}, "cndigar": {"04", "CH"},

	// Chhattisgarh (22, CG)
	"chhattisgarh": {"22", "CG"}, "ct": {"22", "CG"}, "chatisgar": {"22", "CG"}, "chhatisgar": {"22", "CG"}, "chattisgarh": {"22", "CG"},

	// Dadra & Nagar Haveli (26, DN)
	"dadra & nagar haveli": {"26", "DN"}, "dadra and nagar haveli": {"26", "DN"}, "dadra & nagar": {"26", "DN"}, "dadra and nagar": {"26", "DN"}, "dn": {"26", "DN"}, "d.n": {"26", "DN"},

	// Dadra & Nagar Haveli and Daman & Diu (26, DNHDD) - merged UT since Jan 2020; CIBIL maps to 26 (Dadra & Nagar Haveli)
	"dadra & nagar haveli and daman & diu": {"26", "DNHDD"},

	// Daman & Diu (25, DD)
	"daman & diu": {"25", "DD"}, "dd": {"25", "DD"}, "d.d": {"25", "DD"}, "daman and diu": {"25", "DD"},

	// Delhi (07, DL)
	"delhi": {"07", "DL"}, "dl": {"07", "DL"}, "new delhi": {"07", "DL"}, "nayi delhi": {"07", "DL"},

	// Goa (30, GA)
	"goa": {"30", "GA"}, "ga": {"30", "GA"}, "gao": {"30", "GA"},

	// Gujarat (24, GJ)
	"gujarat": {"24", "GJ"}, "gj": {"24", "GJ"}, "guj": {"24", "GJ"}, "gujrat": {"24", "GJ"}, "gujart": {"24", "GJ"},

	// Haryana (06, HR)
	"haryana": {"06", "HR"}, "hr": {"06", "HR"}, "hryana": {"06", "HR"},

	// Himachal Pradesh (02, HP)
	"himachal pradesh": {"02", "HP"}, "hp": {"02", "HP"}, "h.p": {"02", "HP"}, "h.p.": {"02", "HP"}, "himachl prdesh": {"02", "HP"}, "himachl pradesh": {"02", "HP"}, "himachal prdesh": {"02", "HP"},

	// Jammu & Kashmir (01, JK)
	// CRIF uses "Jammu and Kashmir" (with "and"); CIBIL uses "Jammu & Kashmir" (with "&") - both covered.
	"jammu & kashmir": {"01", "JK"}, "jammu and kashmir": {"01", "JK"}, "j&k": {"01", "JK"}, "jammu": {"01", "JK"}, "kashmir": {"01", "JK"},

	// Jharkhand (20, JH)
	"jharkhand": {"20", "JH"}, "jh": {"20", "JH"}, "j.h": {"20", "JH"}, "jhr": {"20", "JH"}, "jhrkhand": {"20", "JH"}, "jhrkhnd": {"20", "JH"},

	// Karnataka (29, KA)
	"karnataka": {"29", "KA"}, "ka": {"29", "KA"}, "krnatak": {"29", "KA"}, "karnatak": {"29", "KA"}, "krnataka": {"29", "KA"},

	// Kerala (32, KL)
	"kerala": {"32", "KL"}, "kl": {"32", "KL"}, "keral": {"32", "KL"},

	// Ladakh (01, LA) - UT since Nov 2019; pincodes starting with 19 map to CIBIL code 01 (same bucket as J&K)
	"ladakh": {"01", "LA"},

	// Lakshadweep (31, LD)
	// CRIF uses both "Lakshadweep Islands" and "Lakshadweep" - both covered.
	"lakshadweep": {"31", "LD"}, "lakshadweep islands": {"31", "LD"}, "ld": {"31", "LD"}, "lakshdwep": {"31", "LD"}, "lakshdweep": {"31", "LD"}, "lakshadwep": {"31", "LD"},

	// Madhya Pradesh (23, MP)
	"madhya pradesh": {"23", "MP"}, "mp": {"23", "MP"}, "m.p": {"23", "MP"}, "madya prdesh": {"23", "MP"}, "madya pradesh": {"23", "MP"}, "madhya prdesh": {"23", "MP"},

	// Maharashtra (27, MH)
	"maharashtra": {"27", "MH"}, "mh": {"27", "MH"}, "mharastra": {"27", "MH"}, "mharashtra": {"27", "MH"},

	// Manipur (14, MN)
	"manipur": {"14", "MN"}, "mn": {"14", "MN"}, "m.n": {"14", "MN"}, "mnipur": {"14", "MN"},

	// Meghalaya (17, ML)
	"meghalaya": {"17", "ML"}, "ml": {"17", "ML"}, "m.l": {"17", "ML"}, "meghlay": {"17", "ML"}, "meghalay": {"17", "ML"}, "meghlaya": {"17", "ML"},

	// Mizoram (15, MZ)
	"mizoram": {"15", "MZ"}, "mz": {"15", "MZ"}, "m.z": {"15", "MZ"}, "mizo": {"15", "MZ"}, "mizorm": {"15", "MZ"},

	// Nagaland (13, NL)
	"nagaland": {"13", "NL"}, "nl": {"13", "NL"}, "n.l": {"13", "NL"}, "ngaland": {"13", "NL"},

	// Orissa (21, OR)
	"orissa": {"21", "OR"}, "or": {"21", "OR"}, "odissa": {"21", "OR"}, "odisha": {"21", "OR"}, "orisa": {"21", "OR"}, "orrisa": {"21", "OR"},

	// Pondicherry (34, PY)
	"pondicherry": {"34", "PY"}, "py": {"34", "PY"}, "puducherry": {"34", "PY"}, "punducherry": {"34", "PY"},

	// Punjab (03, PB)
	"punjab": {"03", "PB"}, "punj": {"03", "PB"}, "pb": {"03", "PB"}, "pnjb": {"03", "PB"},

	// Rajasthan (08, RJ)
	"rajasthan": {"08", "RJ"}, "raj": {"08", "RJ"}, "rj": {"08", "RJ"}, "rajestan": {"08", "RJ"}, "rajesthan": {"08", "RJ"},

	// Sikkim (11, SK)
	"sikkim": {"11", "SK"}, "sk": {"11", "SK"}, "sikim": {"11", "SK"}, "skkim": {"11", "SK"}, "sikm": {"11", "SK"},

	// Tamil Nadu (33, TN)
	"tamil nadu": {"33", "TN"}, "tn": {"33", "TN"}, "t.n": {"33", "TN"}, "tamil naidu": {"33", "TN"},

	// Telangana (36, TS)
	"telangana": {"36", "TS"}, "ts": {"36", "TS"}, "tg": {"36", "TS"}, "telengana": {"36", "TS"}, "telangna": {"36", "TS"}, "telengna": {"36", "TS"},

	// Tripura (16, TR)
	"tripura": {"16", "TR"}, "tr": {"16", "TR"}, "t.r": {"16", "TR"}, "trepura": {"16", "TR"}, "tripora": {"16", "TR"},

	// Uttar Pradesh (09, UP)
	"uttar pradesh": {"09", "UP"}, "up": {"09", "UP"}, "u.p": {"09", "UP"}, "utar pradesh": {"09", "UP"}, "utter pradesh": {"09", "UP"}, "uttar pardesh": {"09", "UP"},

	// Uttaranchal/Uttarakhand (05, UK)
	"uttaranchal": {"05", "UK"}, "utranchal": {"05", "UK"}, "utranchl": {"05", "UK"}, "uttranchl": {"05", "UK"}, "uttaranchl": {"05", "UK"}, "uttranchal": {"05", "UK"}, "uttarakhand": {"05", "UK"}, "ul": {"05", "UK"}, "utrakhand": {"05", "UK"},

	// West Bengal (19, WB)
	"west bengal": {"19", "WB"}, "wb": {"19", "WB"}, "w.b": {"19", "WB"}, "w.b.": {"19", "WB"}, "west bangal": {"19", "WB"}, "bangal": {"19", "WB"}, "pachim bangal": {"19", "WB"},
}

// customResolveBureauStateCode resolves state name to bureau-specific code
// Enhanced version: Supports multiple state parameters with fallback
func (ep *ExpressionProcessor) customResolveBureauStateCode(params []string) string {
	if len(params) < 2 {
		return ""
	}

	// Bureau type is always the last parameter
	bureauType := strings.ToUpper(strings.TrimSpace(params[len(params)-1]))

	// Try each state parameter until we find a non-empty one
	var stateName string
	for i := 0; i < len(params)-1; i++ {
		if params[i] != "" {
			stateName = strings.ToLower(strings.TrimSpace(params[i]))
			break
		}
	}

	if stateName == "" {
		// No state found, return default values
		switch bureauType {
		case "CIBIL":
			return "99"
		case "CRIF":
			return "NA"
		default:
			return ""
		}
	}

	// Direct O(1) hash lookup with compile-time constants
	if codes, exists := stateToCodeMap[stateName]; exists {
		switch bureauType {
		case "CIBIL":
			return codes.CIBIL
		case "CRIF":
			return codes.CRIF
		}
	}

	// No match found, return default values based on bureau type
	switch bureauType {
	case "CIBIL":
		return "99"
	case "CRIF":
		return "NA"
	default:
		return ""
	}
}

// customSplitName splits full name into firstName, middleName, lastName using business rules
func (ep *ExpressionProcessor) customSplitName(params []string) string {
	if len(params) < 2 || params[0] == "" {
		return ""
	}

	fullName := strings.TrimSpace(params[0])
	part := strings.ToLower(strings.TrimSpace(params[1])) // "first", "middle", "last"

	// Split on whitespace and remove empty parts
	nameParts := strings.Fields(fullName)

	switch len(nameParts) {
	case 1:
		switch part {
		case "first":
			return nameParts[0]
		case "last":
			return "."
		case "middle":
			return ""
		}
	case 2:
		switch part {
		case "first":
			return nameParts[0]
		case "last":
			return nameParts[1]
		case "middle":
			return ""
		}
	case 3:
		switch part {
		case "first":
			return nameParts[0]
		case "middle":
			return nameParts[1]
		case "last":
			return nameParts[2]
		}
	case 4:
		switch part {
		case "first":
			return nameParts[0] + " " + nameParts[1]
		case "middle":
			return nameParts[2]
		case "last":
			return nameParts[3]
		}
	case 5:
		switch part {
		case "first":
			return nameParts[0] + " " + nameParts[1]
		case "middle":
			return nameParts[2]
		case "last":
			return nameParts[3] + " " + nameParts[4]
		}
	case 6:
		switch part {
		case "first":
			return nameParts[0] + " " + nameParts[1]
		case "middle":
			return nameParts[2] + " " + nameParts[3]
		case "last":
			return nameParts[4] + " " + nameParts[5]
		}
	case 7:
		switch part {
		case "first":
			return nameParts[0] + " " + nameParts[1] + " " + nameParts[2]
		case "middle":
			return nameParts[3] + " " + nameParts[4]
		case "last":
			return nameParts[5] + " " + nameParts[6]
		}
	case 8:
		switch part {
		case "first":
			return nameParts[0] + " " + nameParts[1] + " " + nameParts[2]
		case "middle":
			return nameParts[3] + " " + nameParts[4] + " " + nameParts[5]
		case "last":
			return nameParts[6] + " " + nameParts[7]
		}
	case 9:
		switch part {
		case "first":
			return nameParts[0] + " " + nameParts[1] + " " + nameParts[2]
		case "middle":
			return nameParts[3] + " " + nameParts[4] + " " + nameParts[5]
		case "last":
			return nameParts[6] + " " + nameParts[7] + " " + nameParts[8]
		}
	default: // 10 or more parts (same as 9-part rule)
		switch part {
		case "first":
			return nameParts[0] + " " + nameParts[1] + " " + nameParts[2]
		case "middle":
			return nameParts[3] + " " + nameParts[4] + " " + nameParts[5]
		case "last":
			return nameParts[6] + " " + nameParts[7] + " " + nameParts[8]
		}
	}
	return ""
}

// customMapGender maps various gender representations to standard codes
func (ep *ExpressionProcessor) customMapGender(params []string) string {
	if len(params) < 1 || params[0] == "" {
		return ""
	}

	gender := strings.ToLower(strings.TrimSpace(params[0]))

	switch gender {
	case "m", "male":
		return "2"
	case "f", "female":
		return "1"
	default:
		return ""
	}
}

// customMapDocumentType maps document length to type codes
func (ep *ExpressionProcessor) customMapDocumentType(params []string) string {
	// Try each document field until one has a value
	for _, param := range params {
		if param != "" {
			length := len(strings.TrimSpace(param))
			switch length {
			case 10:
				return "01"
			case 12:
				return "06"
			case 16:
				return "04"
			}
		}
	}
	return ""
}

// Helper methods for FOIR calculation
func (ep *ExpressionProcessor) determineIncomeToUse(declaredIncome, incomeModelPrediction float64) float64 {
	// Priority 1: Income Model Prediction (highest priority)
	if incomeModelPrediction > 0 {
		return incomeModelPrediction
	}
	// Priority 2: Declared income (fallback)
	if declaredIncome > 0 {
		return declaredIncome
	}
	// Fallback: If all are null/zero, use declared income even if zero
	return declaredIncome
}

func (ep *ExpressionProcessor) calculateAverageSalary(declaredIncome, incomeToUse float64, foirOnImputedIncome bool, leadSource string) float64 {
	if leadSource == "Rentickle" || leadSource == "Reliance" {
		return incomeToUse
	} else if !foirOnImputedIncome {
		if leadSource == "Finnable" || leadSource == "MoneyView" {
			return math.Min(declaredIncome, incomeToUse)
		} else {
			return declaredIncome
		}
	} else {
		return math.Min(declaredIncome, incomeToUse)
	}
}

func (ep *ExpressionProcessor) calculateExistingObligations(existingAccountsJSON string) float64 {
	if existingAccountsJSON == "" || existingAccountsJSON == "[]" {
		return 0
	}

	var accounts []map[string]interface{}
	if err := json.Unmarshal([]byte(existingAccountsJSON), &accounts); err != nil {
		return 0
	}

	totalObligations := 0.0
	for _, account := range accounts {
		accountEMI := 0.0

		// Extract account type
		acctType, _ := account["Acct_Type__c"].(string)
		accountType, _ := account["Account_Type__c"].(string)
		highCredit, _ := account["High_Credit_or_Sanctioned_Amount__c"].(float64)
		ownershipIndicator, _ := account["Ownership_Indicator__c"].(string)

		// Calculate EMI based on account type
		if acctType == "Personal Loan" || accountType == "05" {
			accountEMI = ep.calculatePersonalLoanEMI(highCredit)
		} else if acctType == "Consumer Loan" || accountType == "06" {
			accountEMI = ep.calculateConsumerLoanEMI(highCredit)
		} else if acctType == "2-Wheeler Loan" || accountType == "13" {
			accountEMI = ep.calculateTwoWheelerLoanEMI(highCredit)
		} else if acctType == "Auto Loan" || accountType == "01" {
			accountEMI = ep.calculateAutoLoanEMI(highCredit)
		} else if acctType == "Business Loan" || accountType == "40" {
			accountEMI = ep.calculateBusinessLoanEMI(highCredit)
		} else if acctType == "Education Loan" || accountType == "08" {
			accountEMI = ep.calculateEducationLoanEMI(highCredit)
		} else if acctType == "Housing Loan" || accountType == "02" {
			accountEMI = ep.calculateHousingLoanEMI(highCredit)
		} else if acctType == "Gold Loan" || accountType == "07" {
			accountEMI = highCredit * 0.01 // 1% of balance
		} else if acctType == "Overdraft" || accountType == "12" {
			accountEMI = highCredit * 0.01 // 1% of balance
		} else if acctType == "Credit Card" || accountType == "10" {
			// Credit cards are handled separately in FOIR calculation
			continue
		}

		// Apply ownership indicator
		switch ownershipIndicator {
		case "4":
			accountEMI = accountEMI / 2 // Joint account
		case "3":
			accountEMI = 0 // Guarantor
		}

		totalObligations += accountEMI
	}

	return totalObligations
}

func (ep *ExpressionProcessor) calculatePersonalLoanEMI(highCredit float64) float64 {
	var tenor int
	rate := 18.0

	if highCredit >= 300000 {
		tenor = 48
	} else if highCredit >= 50000 {
		tenor = 36
	} else {
		tenor = 12
	}

	return ep.calculateEMI(tenor, rate, highCredit)
}

func (ep *ExpressionProcessor) calculateConsumerLoanEMI(highCredit float64) float64 {
	var tenor int
	rate := 25.0

	if highCredit >= 25000 {
		tenor = 12
	} else {
		tenor = 6
	}

	return ep.calculateEMI(tenor, rate, highCredit)
}

func (ep *ExpressionProcessor) calculateTwoWheelerLoanEMI(highCredit float64) float64 {
	var tenor int
	rate := 21.0

	if highCredit >= 50000 {
		tenor = 36
	} else {
		tenor = 24
	}

	return ep.calculateEMI(tenor, rate, highCredit)
}

func (ep *ExpressionProcessor) calculateAutoLoanEMI(highCredit float64) float64 {
	var tenor int
	rate := 12.0

	if highCredit >= 300000 {
		tenor = 48
	} else {
		tenor = 36
	}

	return ep.calculateEMI(tenor, rate, highCredit)
}

func (ep *ExpressionProcessor) calculateBusinessLoanEMI(highCredit float64) float64 {
	var tenor int
	rate := 18.0

	if highCredit >= 500000 {
		tenor = 36
	} else {
		tenor = 24
	}

	return ep.calculateEMI(tenor, rate, highCredit)
}

func (ep *ExpressionProcessor) calculateEducationLoanEMI(highCredit float64) float64 {
	tenor := 60
	rate := 12.0

	return ep.calculateEMI(tenor, rate, highCredit)
}

func (ep *ExpressionProcessor) calculateHousingLoanEMI(highCredit float64) float64 {
	var tenor int
	rate := 10.0

	if highCredit >= 1500000 {
		tenor = 240
	} else {
		tenor = 180
	}

	return ep.calculateEMI(tenor, rate, highCredit)
}

func (ep *ExpressionProcessor) calculateEMI(tenor int, rate, principal float64) float64 {
	if principal <= 0 || tenor <= 0 || rate <= 0 {
		return 0
	}

	monthlyRate := rate / 1200
	emi := (principal * monthlyRate * math.Pow(1+monthlyRate, float64(tenor))) /
		(math.Pow(1+monthlyRate, float64(tenor)) - 1)

	return emi
}

func (ep *ExpressionProcessor) calculateProposedEMI(proposedEMI, loanAmount, loanRate float64, loanTenor int, leadSource string) float64 {
	// If proposed EMI is already provided and valid, use it
	if proposedEMI > 0 {
		return proposedEMI
	}

	// Calculate EMI using standard formula
	if leadSource == "Reliance" {
		if loanTenor > 0 {
			return loanAmount / float64(loanTenor)
		}
		return 0
	} else {
		if loanAmount > 0 && loanRate > 0 && loanTenor > 0 {
			monthlyRate := loanRate / 1200
			emi := (loanAmount * monthlyRate * math.Pow(1+monthlyRate, float64(loanTenor))) /
				(math.Pow(1+monthlyRate, float64(loanTenor)) - 1)
			return emi
		} else {
			return 0 // Cannot calculate EMI with invalid parameters
		}
	}
}

func (ep *ExpressionProcessor) calculateFOIRValue(existingObligations, proposedEMI, creditCardIndebtedness, averageSalary float64) float64 {
	if averageSalary <= 0 {
		return -1 // Invalid case
	}

	totalObligations := existingObligations + proposedEMI
	creditCardObligation := creditCardIndebtedness * 0.05 // 5% of credit card balance

	foir := ((totalObligations + creditCardObligation) / averageSalary) * 100
	return foir
}

// ============================================================================
// UTILITY METHODS
// ============================================================================

// reverseString reverses a string
func (ep *ExpressionProcessor) reverseString(s string) string {
	runes := []rune(s)
	for i, j := 0, len(runes)-1; i < j; i, j = i+1, j-1 {
		runes[i], runes[j] = runes[j], runes[i]
	}
	return string(runes)
}

// formatNumber formats a number with optional precision
func (ep *ExpressionProcessor) formatNumber(value float64, precision int) string {
	if precision < 0 {
		// Auto-detect precision
		if value == float64(int(value)) {
			return fmt.Sprintf("%.0f", value)
		}
		return fmt.Sprintf("%g", value)
	}
	format := fmt.Sprintf("%%.%df", precision)
	return fmt.Sprintf(format, value)
}

// parseCommaSeparatedArgs parses comma-separated arguments
func (ep *ExpressionProcessor) parseCommaSeparatedArgs(argsStr string) []string {
	if argsStr == "" {
		return []string{}
	}

	args := strings.Split(argsStr, ",")
	for i := range args {
		args[i] = strings.TrimSpace(args[i])
	}
	return args
}

// isTopLevelFallback checks if || appears at top level
func (ep *ExpressionProcessor) isTopLevelFallback(text string) bool {
	if !strings.Contains(text, "||") {
		return false
	}

	depth := 0
	inQuotes := false
	var quoteChar byte

	for i := 0; i < len(text)-1; i++ {
		char := text[i]
		nextChar := text[i+1]

		switch char {
		case '\\':
			if inQuotes {
				i++ // skip escaped char (\' / \\) inside quotes so it can't toggle quote state
			}
		case '\'', '"':
			// Track which quote opened the region so the other quote type inside it (e.g. a " inside
			// a '...' JSON-bearing literal) does not falsely toggle the state.
			if !inQuotes {
				inQuotes = true
				quoteChar = char
			} else if char == quoteChar {
				inQuotes = false
			}
		case '{':
			if !inQuotes && nextChar == '{' {
				depth++
				i++ // skip the next '{'
			}
		case '}':
			if !inQuotes && nextChar == '}' {
				depth--
				i++ // skip the next '}'
			}
		case '|':
			if !inQuotes && depth == 0 && nextChar == '|' {
				return true
			}
		}
	}

	return false
}

// isNumericLiteral checks if string is numeric literal
func (ep *ExpressionProcessor) isNumericLiteral(text string) bool {
	if text == "" {
		return false
	}
	_, err := strconv.ParseFloat(text, 64)
	return err == nil
}

// parsedTransformSpec holds the directives parsed out of a single ARRAY transform spec
// (the params segment of {{ARRAY:source:transform-only:<params>}}).
type parsedTransformSpec struct {
	// fields maps a source field path (nested dot paths supported) to its output key,
	// e.g. "VALUE" → "telephoneNumber", "result.pradr.adr" → "td_principal_place_of_business".
	fields map[string]string
	// types maps an output key to its @TYPE cast, e.g. "telephoneNumber" → "STRING".
	types map[string]string
	// statics maps an output key to a constant injected on every element, e.g. "telephoneType" → "00".
	statics map[string]string
	// indexes maps an output key to the base of a positional injection
	// (1 for __rownum__, 0 for __index__).
	indexes map[string]int
	// defaults maps an output key to its `??` per-element default. Presence in this map is what
	// enables the default, so a mapping without `??` keeps its v2.3 null / "" / absent behaviour.
	defaults map[string]transformDefault
}

func newParsedTransformSpec() *parsedTransformSpec {
	return &parsedTransformSpec{
		fields:   make(map[string]string),
		types:    make(map[string]string),
		statics:  make(map[string]string),
		indexes:  make(map[string]int),
		defaults: make(map[string]transformDefault),
	}
}

// splitTransformPairs splits a transform spec into mapping pairs on commas that sit outside
// quotes. Quote awareness exists so a quoted value containing a comma survives the split, whether
// it is a `??` default (Status__c->status??'N/A, unknown') or a static literal ('a,b'->tag).
//
// If the spec ends with an unbalanced quote the quote-aware pass is discarded and the
// historical naive comma split is returned, so a stray apostrophe in an existing config can
// never change its behaviour.
//
// The scanning itself lives in utility so that SOQL field extraction
// (utility.extractFieldsFromArrayTransformSpec) tokenises the spec exactly the same way. If the
// two ever disagreed, the fetched columns would stop matching the requested mappings.
func splitTransformPairs(transformSpec string) []string {
	return utility.SplitTransformSpecUnquoted(transformSpec, ',')
}

// indexUnquotedToken returns the index of the first occurrence of token in text that lies outside
// any quoted region, or -1 when there is none.
func indexUnquotedToken(text, token string) int {
	return utility.IndexUnquotedToken(text, token)
}

// splitMappingDefault splits a mapping pair on the first UNQUOTED occurrence of "??".
// The left segment is the pre-existing mapping spec (source->target@TYPE) and is handed to the
// unchanged parse path; the right segment is the default literal. When no unquoted "??" is present
// the pair is returned untouched and hasDefault is false, which keeps v2.3 behaviour byte-identical.
//
// Skipping quoted regions is what lets "??" be used as data rather than as the operator:
//
//	'??'->tag              injects the literal string "??" on every element (no default)
//	Note__c->note??'??'    defaults the note to the literal string "??"
//
// Note that only the CONFIG spec is parsed here. A "??" appearing in the response data or in a
// Salesforce field VALUE is never seen by this function, so data can never affect the mapping.
func splitMappingDefault(pair string) (spec string, defaultValue transformDefault, hasDefault bool) {
	separatorIndex := indexUnquotedToken(pair, "??")
	if separatorIndex < 0 {
		return pair, transformDefault{}, false
	}
	// `??` with nothing after it is a config typo, not a default. Treating it as one would inject
	// an empty string (even on a @NUMERIC field), which is not what "default" can reasonably mean.
	// The empty `??` segment is still stripped so it cannot leak into the target key or the @TYPE
	// annotation; only the default registration is skipped, so null / "" / absent behave exactly as
	// they do without `??`. Use ??'' when an empty-string default is genuinely wanted.
	if strings.TrimSpace(pair[separatorIndex+2:]) == "" {
		return pair[:separatorIndex], transformDefault{}, false
	}
	return pair[:separatorIndex], parseDefaultLiteral(pair[separatorIndex+2:]), true
}

// transformDefault is a parsed `??` default literal. How the literal was WRITTEN is retained, not
// just its text, because that is what decides the output type when the mapping's declared @TYPE
// cannot hold the value (see resolve).
type transformDefault struct {
	// text is the literal with any surrounding quote pair removed.
	text string
	// quoted records that the literal was written inside quotes, which pins it to a string.
	quoted bool
	// isNull records the bare `null` literal, which becomes a JSON null under any declared type.
	isNull bool
}

// parseDefaultLiteral normalises the literal to the right of "??":
//   - a matched pair of surrounding single or double quotes is stripped and remembered; quoting is
//     what lets a default contain a comma (??'N/A, unknown') and what pins it to a string
//   - bare unquoted "null" (any case) becomes an explicit JSON null
//   - everything else is kept as raw text and typed at output time by resolve
func parseDefaultLiteral(raw string) transformDefault {
	literal := strings.TrimSpace(raw)
	if len(literal) >= 2 && (literal[0] == '\'' || literal[0] == '"') && literal[len(literal)-1] == literal[0] {
		return transformDefault{text: literal[1 : len(literal)-1], quoted: true}
	}
	if strings.EqualFold(literal, "null") {
		return transformDefault{isNull: true}
	}
	return transformDefault{text: literal}
}

// resolve renders the default for output under the mapping's declared @TYPE, and reports whether
// the declared type could not hold it.
//
//	??null                  -> JSON null, whatever the declared type
//	@TYPE fits the literal   -> the converted value (@NUMERIC??-999 -> the number -999)
//	@TYPE cannot hold it     -> the literal in its own natural JSON type. A plain cast would
//	                            silently yield null for @NUMERIC or false for @BOOLEAN and throw
//	                            the sentinel away, so the literal is preserved instead.
//	no @TYPE                 -> the literal as a string, matching the untyped source values
func (d transformDefault) resolve(targetType string) (value interface{}, crossType bool) {
	if d.isNull {
		return nil, false
	}
	if strings.TrimSpace(targetType) == "" {
		return d.text, false
	}
	if converted, ok := convertDefaultForType(d.text, targetType); ok {
		return converted, false
	}
	return d.naturalValue(), true
}

// naturalValue types the literal by the way it was written: quoting pins it to a string, a bare
// number becomes a JSON number, a bare true/false becomes a JSON boolean, anything else is a string.
func (d transformDefault) naturalValue() interface{} {
	if d.quoted {
		return d.text
	}

	literal := strings.TrimSpace(d.text)
	if num, err := strconv.ParseFloat(literal, 64); err == nil {
		if num == float64(int(num)) {
			return int(num)
		}
		return num
	}
	switch strings.ToLower(literal) {
	case "true":
		return true
	case "false":
		return false
	}
	return d.text
}

// convertDefaultForType applies targetType to a default literal and reports whether the conversion
// was meaningful. It deliberately does NOT reuse applyTypeConversion, which is built for source
// values and invents one: applyTypeConversion turns any unparseable value into null under @NUMERIC
// and into false under @BOOLEAN. For a default that would discard the sentinel the config author
// wrote, so here an incompatible literal is rejected and the caller keeps it verbatim.
func convertDefaultForType(literal, targetType string) (interface{}, bool) {
	trimmed := strings.TrimSpace(literal)

	// Matched case-SENSITIVELY, exactly as applyTypeConversion does for source values. Normalising
	// here instead would split one field's output: a lowercase `@numeric` would leave a populated
	// source as the string "7" (applyTypeConversion's default arm) while converting the default to
	// the number -999. Both paths must agree on what counts as a type annotation.
	switch strings.TrimSpace(targetType) {
	case "NUMERIC":
		num, err := strconv.ParseFloat(trimmed, 64)
		if err != nil {
			return nil, false
		}
		if num == float64(int(num)) {
			return int(num), true
		}
		return num, true
	case "BOOLEAN":
		// Only an explicit truthy or falsy token converts. Anything else (e.g. 'N/A') is rejected
		// rather than silently becoming false.
		switch strings.ToLower(trimmed) {
		case "true", "1", "yes", "on", "enabled":
			return true, true
		case "false", "0", "no", "off", "disabled":
			return false, true
		}
		return nil, false
	case "STRING":
		return literal, true
	default:
		// Unknown type annotation: leave the literal alone, mirroring applyTypeConversion's default arm.
		return literal, true
	}
}

// isBlankTransformValue reports whether a resolved element value should be replaced by its
// `??` default. A nil covers both an explicit JSON null and a missing key or missing nested
// path (navigateNestedFieldRaw returns nil for both). Blank strings are included to match the
// `||` fallback chain, which also treats empty as absent.
func isBlankTransformValue(value interface{}) bool {
	if value == nil {
		return true
	}
	if str, ok := value.(string); ok {
		return strings.TrimSpace(str) == ""
	}
	return false
}

// parseTransformMap parses a transform specification into a parsedTransformSpec.
//
// Supported mapping-pair forms:
//
//	Score__c->value                       plain rename
//	Score__c->value@NUMERIC               rename with type cast
//	result.pradr.adr->address             nested source path
//	'00'->telephoneType@STRING            static literal injected on every item
//	__rownum__->key / __index__->pos      element position (1-based / 0-based)
//	Decile__c->value@NUMERIC??-999        per-element default when the source is null/empty/absent
//
// Static literals are expressed by single-quoting the source side of the mapping:
//
//	'00'->telephoneType@STRING   injects "telephoneType":"00" into every array item
//	'00'->telephoneType          same but no explicit type cast (value stays as string)
// indexTokenBase reports whether a transform source token is an array-position
// injector and returns its starting base. "__index__" is 0-based; "__rownum__"
// is 1-based. These let a transform emit the element's position as a field
// (e.g. __rownum__->key yields key: 1,2,3,...), which the source field data
// itself cannot provide.
func indexTokenBase(from string) (int, bool) {
	switch strings.ToLower(strings.TrimSpace(from)) {
	case "__rownum__":
		return 1, true
	case "__index__":
		return 0, true
	}
	return 0, false
}

func (ep *ExpressionProcessor) parseTransformMap(transformSpec string) *parsedTransformSpec {
	spec := newParsedTransformSpec()
	pairs := splitTransformPairs(transformSpec)

	for _, rawPair := range pairs {
		// Peel off an optional ??default suffix BEFORE any other parsing, so a default that
		// happens to contain '@' or '->' cannot disturb the type / arrow splits below.
		pair, defaultValue, hasDefault := splitMappingDefault(rawPair)

		// registerDefault attaches the default to a real field mapping only. Static literals and
		// positional injections always have a value, so a default there is meaningless.
		registerDefault := func(to string) {
			if hasDefault {
				spec.defaults[to] = defaultValue
			}
		}

		if strings.Contains(pair, "->") {
			// Check if this pair has type specification using @ separator
			if strings.Contains(pair, "@") {
				// Format: source->newField@type  (source may be a quoted literal)
				atParts := strings.Split(pair, "@")
				if len(atParts) == 2 {
					fieldMapping := strings.TrimSpace(atParts[0])
					targetType := strings.TrimSpace(atParts[1])

					if strings.Contains(fieldMapping, "->") {
						arrowParts := strings.Split(fieldMapping, "->")
						if len(arrowParts) == 2 {
							from := strings.TrimSpace(arrowParts[0])
							to := strings.TrimSpace(arrowParts[1])
							if base, isIdx := indexTokenBase(from); isIdx {
								// Row-number / index injection (e.g. __rownum__->key)
								spec.indexes[to] = base
								if targetType != "" {
									spec.types[to] = targetType
								}
							} else if utility.IsQuotedLiteral(from) {
								// Quoted literal ('00' or "00"): inject constant value. Both quote
								// styles are accepted so this agrees with splitTransformPairs and
								// parseDefaultLiteral, which already treat " as a quote.
								spec.statics[to] = utility.UnquoteLiteral(from)
								spec.types[to] = targetType
							} else {
								spec.fields[from] = to
								spec.types[to] = targetType
								registerDefault(to)
							}
						}
					}
				}
			} else {
				// Format: source->newField  (no type conversion; source may be a quoted literal)
				parts := strings.Split(pair, "->")
				if len(parts) == 2 {
					from := strings.TrimSpace(parts[0])
					to := strings.TrimSpace(parts[1])
					if base, isIdx := indexTokenBase(from); isIdx {
						// Row-number / index injection (e.g. __rownum__->key)
						spec.indexes[to] = base
					} else if utility.IsQuotedLiteral(from) {
						// Quoted literal ('00' or "00"): inject constant value.
						spec.statics[to] = utility.UnquoteLiteral(from)
					} else {
						spec.fields[from] = to
						registerDefault(to)
					}
				}
			}
		}
	}

	ep.warnCrossTypeDefaults(spec)
	return spec
}

// warnCrossTypeDefaults logs each `??` default whose declared @TYPE cannot hold it, so a config typo
// such as @NUMERIC??N/A is visible in the logs rather than only in the emitted payload. The value is
// still preserved (see transformDefault.resolve) — this is a warning, not a rejection.
//
// Called once per parsed spec rather than from the transform loop, which runs per element per field:
// a 500-record array would otherwise produce 500 identical lines.
func (ep *ExpressionProcessor) warnCrossTypeDefaults(spec *parsedTransformSpec) {
	if ep == nil || ep.logger == nil || spec == nil {
		return
	}
	for targetKey, defaultValue := range spec.defaults {
		targetType := spec.types[targetKey]
		if strings.TrimSpace(targetType) == "" {
			continue
		}
		if resolved, crossType := defaultValue.resolve(targetType); crossType {
			ep.logWarn("ARRAY transform ?? default does not match its declared @TYPE; emitting the default in its own JSON type",
				"targetKey", targetKey,
				"declaredType", targetType,
				"default", defaultValue.text,
				"emittedAs", fmt.Sprintf("%T", resolved))
		}
	}
}

// transformArray transforms array fields with optional type conversion.
// Source paths in spec.fields support nested dot paths (e.g. "result.ctb", "result.pradr.adr").
// spec.statics injects constant values onto every output item (e.g. "telephoneType" → "00").
// spec.defaults supplies a per-element `??` default when the resolved source value is null,
// blank, or absent; the default is substituted BEFORE @TYPE conversion so @NUMERIC??-999
// yields the JSON number -999.
func (ep *ExpressionProcessor) transformArray(sourceArray []interface{}, spec *parsedTransformSpec, onlyTransformed bool) []interface{} {
	result := make([]interface{}, len(sourceArray))
	if spec == nil {
		spec = newParsedTransformSpec()
	}

	for i, item := range sourceArray {
		if itemMap, ok := item.(map[string]interface{}); ok {
			newItem := make(map[string]interface{})

			// Resolve each transform mapping by path (supports nested paths like result.ctb, result.pradr.adr)
			for sourcePath, targetKey := range spec.fields {
				parts := strings.Split(sourcePath, ".")
				for j := range parts {
					parts[j] = strings.TrimSpace(parts[j])
				}
				value := ep.navigateNestedFieldRaw(itemMap, parts)

				// Per-element default (??). Only consulted when the pair actually declared one,
				// so mappings without ?? keep their v2.3 null / "" / absent behaviour exactly.
				// The default carries its own typing rules (resolve), which is why it does not
				// fall through to applyTypeConversion below: a cross-type default such as
				// @NUMERIC??'N/A' must survive as "N/A" rather than be cast away to null.
				if defaultValue, hasDefault := spec.defaults[targetKey]; hasDefault && isBlankTransformValue(value) {
					// Cross-type defaults are reported once at parse time by warnCrossTypeDefaults,
					// not here: this runs per element per field, so logging it here would emit one
					// line per array element on a large record set.
					resolved, _ := defaultValue.resolve(spec.types[targetKey])
					newItem[targetKey] = resolved
					continue
				}

				if targetType, hasType := spec.types[targetKey]; hasType {
					value = ep.applyTypeConversion(value, targetType)
				}
				newItem[targetKey] = value
			}

			// Inject static/constant values (e.g. 'telephoneType': '00' → every item gets "telephoneType":"00")
			for targetKey, literal := range spec.statics {
				var value interface{} = literal
				if targetType, hasType := spec.types[targetKey]; hasType {
					value = ep.applyTypeConversion(literal, targetType)
				}
				newItem[targetKey] = value
			}

			// Inject the element's array position (row number / index) — e.g. __rownum__->key => key:1,2,3.
			// Base is 1 for __rownum__ and 0 for __index__ (set in parseTransformMap).
			for targetKey, base := range spec.indexes {
				var value interface{} = i + base
				if targetType, hasType := spec.types[targetKey]; hasType {
					value = ep.applyTypeConversion(i+base, targetType)
				}
				newItem[targetKey] = value
			}

			if !onlyTransformed {
				// Copy keys not transformed (backward compat: top-level key match)
				for key, value := range itemMap {
					// Skip raw transform SOURCE fields (preserves existing behavior: `firstName->name`
					// yields "name" only, not "firstName").
					if _, isTransformSource := spec.fields[key]; isTransformSource {
						continue
					}
					// Protect ONLY row-number/index injections (__rownum__ / __index__) from being
					// clobbered by a same-named original field. These are synthetic positional counters
					// that the source data cannot supply, so an existing field with the same name (e.g.
					// an "id_index" field alongside __rownum__->id_index) must not overwrite them.
					//
					// Deliberately NOT extended to transform targets or static literals: for those the
					// original element field keeps precedence on a name collision, exactly as on main.
					// Widening this guard would silently change existing production configs (e.g. a
					// transform target whose source field is absent would become null instead of falling
					// back to the original same-named field).
					if _, isIndexInjected := spec.indexes[key]; isIndexInjected {
						continue
					}
					newItem[key] = value
				}
			}

			result[i] = newItem
		} else {
			result[i] = item
		}
	}

	return result
}

// filterArray filters array based on condition.
// Returns a non-nil empty slice when no elements match so that json.Marshal produces "[]" not "null".
func (ep *ExpressionProcessor) filterArray(sourceArray []interface{}, condition string) []interface{} {
	result := make([]interface{}, 0)

	for _, item := range sourceArray {
		if itemMap, ok := item.(map[string]interface{}); ok {
			if ep.evaluateFilterCondition(itemMap, condition) {
				result = append(result, item)
			}
		}
	}

	return result
}

// arrayMax returns the maximum element from the array. If numericCompare is true, elements are
// compared numerically (parsing strings to float64); otherwise compared lexicographically.
// Returns nil for empty array. Returned value preserves original representation (e.g. string "100000000002").
func (ep *ExpressionProcessor) arrayMax(sourceArray []interface{}, numericCompare bool) interface{} {
	if len(sourceArray) == 0 {
		return nil
	}
	if numericCompare {
		var maxVal float64
		var maxOriginal interface{}
		first := true
		for _, v := range sourceArray {
			var f float64
			switch x := v.(type) {
			case float64:
				f = x
			case int:
				f = float64(x)
			case int64:
				f = float64(x)
			case string:
				var err error
				f, err = strconv.ParseFloat(strings.TrimSpace(x), 64)
				if err != nil {
					continue
				}
			default:
				continue
			}
			if first || f > maxVal {
				maxVal = f
				maxOriginal = v
				first = false
			}
		}
		if first {
			return nil
		}
		return maxOriginal
	}
	// String comparison (lexicographic)
	maxIdx := 0
	maxStr := ep.arrayElementToString(sourceArray[0])
	for i := 1; i < len(sourceArray); i++ {
		s := ep.arrayElementToString(sourceArray[i])
		if s > maxStr {
			maxStr = s
			maxIdx = i
		}
	}
	return sourceArray[maxIdx]
}

// arrayMin returns the minimum element from the array. If numericCompare is true, elements are
// compared numerically; otherwise lexicographically. Returns nil for empty array.
func (ep *ExpressionProcessor) arrayMin(sourceArray []interface{}, numericCompare bool) interface{} {
	if len(sourceArray) == 0 {
		return nil
	}
	if numericCompare {
		var minVal float64
		var minOriginal interface{}
		first := true
		for _, v := range sourceArray {
			var f float64
			switch x := v.(type) {
			case float64:
				f = x
			case int:
				f = float64(x)
			case int64:
				f = float64(x)
			case string:
				var err error
				f, err = strconv.ParseFloat(strings.TrimSpace(x), 64)
				if err != nil {
					continue
				}
			default:
				continue
			}
			if first || f < minVal {
				minVal = f
				minOriginal = v
				first = false
			}
		}
		if first {
			return nil
		}
		return minOriginal
	}
	// String comparison (lexicographic)
	minIdx := 0
	minStr := ep.arrayElementToString(sourceArray[0])
	for i := 1; i < len(sourceArray); i++ {
		s := ep.arrayElementToString(sourceArray[i])
		if s < minStr {
			minStr = s
			minIdx = i
		}
	}
	return sourceArray[minIdx]
}

// parseMaxMinParams extracts @NUMERIC flag and optional field name from max/min params.
// Params may be ["@NUMERIC"], ["@NUMERIC", "uan"], ["uan"], or [].
func (ep *ExpressionProcessor) parseMaxMinParams(params []string) (numericCompare bool, fieldName string) {
	for _, p := range params {
		t := strings.TrimSpace(p)
		if t == "" {
			continue
		}
		if t == "@NUMERIC" {
			numericCompare = true
		} else if fieldName == "" {
			fieldName = t
		}
	}
	return numericCompare, fieldName
}

// arrayPluckField extracts a field from each element when elements are objects; otherwise uses elements as-is.
// Used so max/min work on both value arrays and arrays of objects (e.g. [{"uan":"1"},{"uan":"2"}] with field "uan" -> ["1","2"]).
func (ep *ExpressionProcessor) arrayPluckField(sourceArray []interface{}, fieldName string) []interface{} {
	if fieldName == "" {
		return sourceArray
	}
	fieldLower := strings.ToLower(strings.TrimSpace(fieldName))
	result := make([]interface{}, 0, len(sourceArray))
	for _, item := range sourceArray {
		if obj, ok := item.(map[string]interface{}); ok {
			var v interface{}
			for k, val := range obj {
				if strings.ToLower(strings.TrimSpace(k)) == fieldLower {
					v = val
					break
				}
			}
			result = append(result, v)
		} else {
			result = append(result, item)
		}
	}
	return result
}

// arrayElementToString converts an array element to string for comparison or output.
func (ep *ExpressionProcessor) arrayElementToString(v interface{}) string {
	if v == nil {
		return ""
	}
	switch x := v.(type) {
	case string:
		return x
	case float64:
		if x == float64(int64(x)) {
			return strconv.FormatInt(int64(x), 10)
		}
		return strconv.FormatFloat(x, 'f', -1, 64)
	case int:
		return strconv.Itoa(x)
	case int64:
		return strconv.FormatInt(x, 10)
	default:
		return fmt.Sprintf("%v", v)
	}
}

// mapArrayFields maps only specified fields
func (ep *ExpressionProcessor) mapArrayFields(sourceArray []interface{}, fields []string) []interface{} {
	result := make([]interface{}, len(sourceArray))

	for i, item := range sourceArray {
		if itemMap, ok := item.(map[string]interface{}); ok {
			newItem := make(map[string]interface{})

			for _, field := range fields {
				if value, exists := itemMap[field]; exists {
					newItem[field] = value
				}
			}

			result[i] = newItem
		} else {
			result[i] = item
		}
	}

	return result
}

// evaluateFilterCondition evaluates filter conditions with optional type conversion support.
// Supports: comparison operators (==, !=, >, <, >=, <=), IN (field IN (val1, val2, ...)),
// AND (cond1 AND cond2), and OR (cond1 OR cond2).
// Field name matching is case-insensitive. This is the shared engine used by both
// ARRAY:filter and the getJsonPath bracket predicate filter.
func (ep *ExpressionProcessor) evaluateFilterCondition(obj map[string]interface{}, condition string) bool {
	condition = strings.TrimSpace(condition)
	if condition == "" {
		return false
	}

	// Handle AND: all sub-conditions must be true (evaluated before OR, higher precedence)
	if strings.Contains(condition, " AND ") {
		andParts := strings.Split(condition, " AND ")
		for _, part := range andParts {
			if !ep.evaluateFilterCondition(obj, strings.TrimSpace(part)) {
				return false
			}
		}
		return true
	}

	// Handle OR: any sub-condition must be true
	if strings.Contains(condition, " OR ") {
		orParts := strings.Split(condition, " OR ")
		for _, part := range orParts {
			if ep.evaluateFilterCondition(obj, strings.TrimSpace(part)) {
				return true
			}
		}
		return false
	}

	// Handle IN operator: "field IN (val1, val2, ...)" or "field@TYPE IN (val1, val2, ...)"
	if strings.Contains(condition, " IN ") {
		parts := strings.SplitN(condition, " IN ", 2)
		if len(parts) == 2 {
			fieldPart := strings.TrimSpace(parts[0])
			listPart := strings.TrimSpace(parts[1])
			listPart = strings.Trim(listPart, "()")
			if listPart == "" {
				return false
			}
			// Parse list values: split by comma, trim, strip quotes
			allowedSet := make(map[string]struct{})
			for _, s := range strings.Split(listPart, ",") {
				v := strings.TrimSpace(s)
				v = strings.Trim(v, "'\"")
				if v != "" {
					allowedSet[v] = struct{}{}
				}
			}
			if len(allowedSet) == 0 {
				return false
			}
			// Parse field name and optional @TYPE
			var fieldName, targetType string
			if strings.Contains(fieldPart, "@") {
				atParts := strings.Split(fieldPart, "@")
				if len(atParts) == 2 {
					fieldName = strings.TrimSpace(atParts[0])
					targetType = strings.TrimSpace(atParts[1])
				}
			} else {
				fieldName = fieldPart
			}
			fieldValue, found := obj[fieldName]
			if !found {
				fieldNameLower := strings.ToLower(fieldName)
				for k, v := range obj {
					if strings.ToLower(strings.TrimSpace(k)) == fieldNameLower {
						fieldValue = v
						found = true
						break
					}
				}
			}
			if !found {
				return false
			}
			if targetType != "" {
				fieldValue = ep.applyTypeConversion(fieldValue, targetType)
			}
			fieldStr := fmt.Sprintf("%v", fieldValue)
			_, inSet := allowedSet[fieldStr]
			return inSet
		}
	}

	operators := []string{">=", "<=", "==", "!=", ">", "<"}

	for _, op := range operators {
		if strings.Contains(condition, op) {
			parts := strings.Split(condition, op)
			if len(parts) == 2 {
				fieldPart := strings.TrimSpace(parts[0])
				expectedValue := strings.Trim(strings.TrimSpace(parts[1]), "'\"")

				// Parse field name and type specification
				var fieldName, targetType string
				if strings.Contains(fieldPart, "@") {
					atParts := strings.Split(fieldPart, "@")
					if len(atParts) == 2 {
						fieldName = strings.TrimSpace(atParts[0])
						targetType = strings.TrimSpace(atParts[1])
					}
				} else {
					fieldName = fieldPart
				}

				if fieldValue, exists := obj[fieldName]; exists {
					// Apply type conversion if specified
					if targetType != "" {
						fieldValue = ep.applyTypeConversion(fieldValue, targetType)
					}

					fieldStr := fmt.Sprintf("%v", fieldValue)

					// Relational comparison with an empty operand -> not-satisfied (false),
					// instead of lexical comparison where "" < "30" would be true. Equality
					// (==/!=) is unaffected; non-empty operands keep lexical comparison.
					if (op == ">" || op == "<" || op == ">=" || op == "<=") && (fieldStr == "" || expectedValue == "") {
						return false
					}

					switch op {
					case "==":
						return fieldStr == expectedValue
					case "!=":
						return fieldStr != expectedValue
					case ">":
						if fVal, err := strconv.ParseFloat(fieldStr, 64); err == nil {
							if eVal, err2 := strconv.ParseFloat(expectedValue, 64); err2 == nil {
								return fVal > eVal
							}
						}
						return fieldStr > expectedValue
					case "<":
						if fVal, err := strconv.ParseFloat(fieldStr, 64); err == nil {
							if eVal, err2 := strconv.ParseFloat(expectedValue, 64); err2 == nil {
								return fVal < eVal
							}
						}
						return fieldStr < expectedValue
					case ">=":
						if fVal, err := strconv.ParseFloat(fieldStr, 64); err == nil {
							if eVal, err2 := strconv.ParseFloat(expectedValue, 64); err2 == nil {
								return fVal >= eVal
							}
						}
						return fieldStr >= expectedValue
					case "<=":
						if fVal, err := strconv.ParseFloat(fieldStr, 64); err == nil {
							if eVal, err2 := strconv.ParseFloat(expectedValue, 64); err2 == nil {
								return fVal <= eVal
							}
						}
						return fieldStr <= expectedValue
					}
				}
			}
			break
		}
	}

	return false
}

// navigateNestedField navigates through nested field structures and returns the value as a string.
// This function implements case-insensitive field matching to handle both camelCase and PascalCase field names.
//
// Case-Insensitive Matching:
//   - First attempts exact match for performance
//   - If exact match fails, performs case-insensitive search
//   - This allows configurations to use camelCase placeholders (e.g., <contact.age1__c>)
//     while data contains PascalCase fields (e.g., "Age1__c")
//   - Resolves issues where field names in database queries don't match placeholder case
//
// Examples:
// - <contact.age1__c> matches "Age1__c" in data
// - <contact.phone> matches "Phone" in data
// - <contact.email> matches "Email" in data
// - <contact.mailingStreet> matches "MailingStreet" in data
//
// Parameters:
// - data: The data structure to navigate (map, array, or primitive)
// - fieldParts: Array of field names to navigate through
//
// Returns:
// - String representation of the final value
// - Empty string if field not found or value is nil
func (ep *ExpressionProcessor) navigateNestedField(data interface{}, fieldParts []string) string {
	current := data

	for _, part := range fieldParts {
		part = strings.TrimSpace(part)

		switch v := current.(type) {
		case map[string]interface{}:
			// Make field lookup case-insensitive by converting to lowercase
			fieldNameLower := strings.ToLower(part)
			var foundValue interface{}
			var found bool

			// First try exact match
			if value, exists := v[part]; exists {
				foundValue = value
				found = true
			} else {
				// If exact match fails, try case-insensitive match with trimmed keys
				// This handles keys with leading/trailing spaces (e.g., "MatchPercentage ")
				for key, value := range v {
					trimmedKey := strings.TrimSpace(key)
					if strings.ToLower(trimmedKey) == fieldNameLower {
						foundValue = value
						found = true
						break
					}
				}
			}

			if found {
				current = foundValue
			} else {
				return ""
			}
		case []interface{}:
			// Handle array access like field[0]
			if strings.Contains(part, "[") && strings.Contains(part, "]") {
				// Parse array index
				indexStart := strings.Index(part, "[")
				indexEnd := strings.Index(part, "]")
				if indexStart < indexEnd {
					indexStr := part[indexStart+1 : indexEnd]
					if index, err := strconv.Atoi(indexStr); err == nil && index >= 0 && index < len(v) {
						current = v[index]
						// Continue with remaining field path if any
						remaining := part[indexEnd+1:]
						if remaining != "" && strings.HasPrefix(remaining, ".") {
							fieldParts = append([]string{remaining[1:]}, fieldParts[1:]...)
						}
						continue
					}
				}
			} else {
				// No explicit array index provided - auto-resolve to first record if available
				if len(v) > 0 {
					current = v[0] // Use first record by default
					continue
				}
			}
			return ""
		default:
			// For final field, convert to string properly handling nil
			if len(fieldParts) == 1 {
				if current == nil {
					return ""
				}
				return formatResolvedScalar(current)
			}
			return ""
		}
	}

	// Convert final result to string, handling nil properly
	if current == nil {
		return ""
	}
	// When the resolved value is a slice/array, return valid JSON so that ARRAY expressions
	// that receive this after ((Service.path)) replacement can parse it (e.g. ((MobileUANService.data.result))).
	if arr, ok := current.([]interface{}); ok {
		if jsonBytes, err := json.Marshal(arr); err == nil {
			return string(jsonBytes)
		}
	}
	return formatResolvedScalar(current)
}

// formatResolvedScalar converts a resolved database field value to its string form. float64
// (the type JSON unmarshalling produces for all numbers) is rendered in shortest plain-decimal
// form via FormatFloat so large/small magnitudes never appear in scientific notation
// (fmt "%v" would emit e.g. "1.234567e+06"). All other types keep the existing "%v" formatting.
// This matches getValueFromItem's float handling and keeps <Object.Field>, <Object[N].Field>, and
// <Object[condition].Field> consistent.
func formatResolvedScalar(v interface{}) string {
	if f, ok := v.(float64); ok {
		return strconv.FormatFloat(f, 'f', -1, 64)
	}
	return fmt.Sprintf("%v", v)
}

// parseArrayIndex reports whether s is a pure non-negative integer array index (e.g. "0", "12")
// and returns its value. A leading sign ("+"/"-"), surrounding whitespace, or any non-digit content
// means it is NOT an index — such bracket contents are treated as filter conditions instead. This
// keeps <Object[0].Field> (positional index) distinct from <Object[Field == 'x'].Field> (filter).
func parseArrayIndex(s string) (int, bool) {
	if s == "" {
		return 0, false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return 0, false
		}
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, false
	}
	return n, true
}

// handleConditionalDbField handles conditional database field access
func (ep *ExpressionProcessor) handleConditionalDbField(placeholder string, masterDTO *common_dto.MasterDTO) string {
	objectName, condition, fieldName, isConditional := utility.ParseConditionalPlaceholder(placeholder)
	if !isConditional {
		return ""
	}

	normalizedObjectName := strings.ToLower(strings.TrimSpace(objectName))
	normalizedCondition := strings.TrimSpace(condition)
	normalizedFieldName := strings.TrimSpace(fieldName)

	if objectData, exists := masterDTO.Data[normalizedObjectName]; exists {
		// Numeric index access: <Object[N].Field> where N is a non-negative integer.
		// This is distinct from a filter condition — index into the object's records array
		// (or, for a single-record bare map, treat index 0 as that record). Out-of-range, or
		// a missing field, resolves to blank. Field lookup uses navigateNestedField so that
		// <Object[0].Field> resolves identically to <Object.Field> (case-insensitive, nested).
		if idx, isIndex := parseArrayIndex(normalizedCondition); isIndex {
			fieldParts := strings.Split(normalizedFieldName, ".")
			if recordsVal, hasRecords := objectData["records"]; hasRecords {
				if records, ok := recordsVal.([]interface{}); ok {
					if idx >= len(records) {
						if debugPipeline {
							fmt.Printf("[DB_INDEX] out-of-range index: object=%s index=%d recordCount=%d -> blank\n",
								normalizedObjectName, idx, len(records))
						}
						return ""
					}
					return ep.navigateNestedField(records[idx], fieldParts)
				}
			}
			// No records array: object stored as a single (bare) record map. Index 0 refers to
			// that record; any higher index is out of range -> blank.
			if idx == 0 {
				return ep.navigateNestedField(objectData, fieldParts)
			}
			if debugPipeline {
				fmt.Printf("[DB_INDEX] out-of-range index on single-record object=%s index=%d -> blank\n",
					normalizedObjectName, idx)
			}
			return ""
		}

		// objectData is map[string]interface{} from MasterDTO.Data structure
		// Note: Arrays from Salesforce are automatically wrapped in {"records": [...]}
		// by the conversion code in sequence_service.go (lines 181-184)
		// So we only need to handle the {"records": [...]} structure here

		// The returned field may be a nested/dotted path (e.g. Multibureau__r.CreatedDate).
		// Resolve it via navigateNestedField so conditional access is nested-aware and
		// case-insensitive — identical to <Object.Field> and <Object[N].Field>.
		fieldParts := strings.Split(normalizedFieldName, ".")

		// Check if it has a "records" array (standard structure: {"records": [...]})
		if recordsVal, hasRecords := objectData["records"]; hasRecords {
			if records, ok := recordsVal.([]interface{}); ok {
				// Iterate through records in order. Return the first matching record whose
				// field resolves to a non-empty value. A matched record whose field is
				// empty/nil does NOT short-circuit — keep scanning subsequent matches, and
				// only return blank once every record has been checked.
				for _, record := range records {
					if recordMap, ok := record.(map[string]interface{}); ok {
						if ep.evaluateConditionOnObject(recordMap, normalizedCondition) {
							if value := ep.navigateNestedField(recordMap, fieldParts); value != "" {
								return value
							}
						}
					}
				}
				// No matching record produced a non-empty value
				return ""
			}
		}

		// If no records array, treat objectData as a single object
		if ep.evaluateConditionOnObject(objectData, normalizedCondition) {
			return ep.navigateNestedField(objectData, fieldParts)
		}
	}

	return ""
}

// evaluateConditionOnObject evaluates condition on a single object (supports multi-conditions)
func (ep *ExpressionProcessor) evaluateConditionOnObject(obj map[string]interface{}, condition string) bool {
	// Handle multi-condition expressions with AND/OR operators
	if strings.Contains(condition, " AND ") {
		parts := strings.Split(condition, " AND ")
		for _, part := range parts {
			if !ep.evaluateSingleConditionOnObject(obj, strings.TrimSpace(part)) {
				return false
			}
		}
		return true
	}

	if strings.Contains(condition, " OR ") {
		parts := strings.Split(condition, " OR ")
		for _, part := range parts {
			if ep.evaluateSingleConditionOnObject(obj, strings.TrimSpace(part)) {
				return true
			}
		}
		return false
	}

	// Single condition
	return ep.evaluateSingleConditionOnObject(obj, condition)
}

// evaluateSingleConditionOnObject evaluates a single condition on an object
func (ep *ExpressionProcessor) evaluateSingleConditionOnObject(obj map[string]interface{}, condition string) bool {
	// Handle IN and NOT IN operators first (they contain spaces)
	if strings.Contains(condition, " NOT IN ") {
		return ep.evaluateNotInCondition(obj, condition)
	}
	if strings.Contains(condition, " IN ") {
		return ep.evaluateInCondition(obj, condition)
	}

	// Parse condition like "Status='Active'" or "Age>18"
	operators := []string{">=", "<=", "==", "!=", ">", "<", "="}

	for _, op := range operators {
		if strings.Contains(condition, op) {
			parts := strings.Split(condition, op)
			if len(parts) == 2 {
				fieldName := strings.TrimSpace(parts[0])
				rawExpected := strings.TrimSpace(parts[1])
				expectedValue := strings.Trim(rawExpected, "'\"")

				// Normalize operator
				if op == "=" {
					op = "=="
				}

				// Case-insensitive field lookup so conditional filters honor ESA's
				// case-insensitive field naming (e.g. [type__c == 'PAN'] matches Type__c).
				fieldValue, exists := lookupFieldCaseInsensitive(obj, fieldName)

				// Null-token handling: an UNQUOTED `null` (case-insensitive) means "no value".
				// A field is null when it is absent, nil, or a blank string. Empty collections
				// ({} / []) are NOT treated as null — configs match those explicitly via
				// == '{}' / == '[]'. A quoted 'null' is a literal string, not the null token.
				if (op == "==" || op == "!=") && rawExpected == expectedValue && strings.EqualFold(expectedValue, "null") {
					fieldIsNull := !exists || fieldValue == nil || fmt.Sprintf("%v", fieldValue) == ""
					if op == "==" {
						return fieldIsNull
					}
					return !fieldIsNull
				}

				if exists {
					fieldStr := fmt.Sprintf("%v", fieldValue)

					// Relational comparison with an empty operand -> not-satisfied (false),
					// instead of lexical comparison where "" < "30" would be true. Equality
					// (==/!=) is unaffected; non-empty operands keep lexical comparison.
					if (op == ">" || op == "<" || op == ">=" || op == "<=") && (fieldStr == "" || expectedValue == "") {
						return false
					}

					switch op {
					case "==":
						return fieldStr == expectedValue
					case "!=":
						return fieldStr != expectedValue
					case ">":
						if fVal, err := strconv.ParseFloat(fieldStr, 64); err == nil {
							if eVal, err2 := strconv.ParseFloat(expectedValue, 64); err2 == nil {
								return fVal > eVal
							}
						}
						return fieldStr > expectedValue
					case "<":
						if fVal, err := strconv.ParseFloat(fieldStr, 64); err == nil {
							if eVal, err2 := strconv.ParseFloat(expectedValue, 64); err2 == nil {
								return fVal < eVal
							}
						}
						return fieldStr < expectedValue
					case ">=":
						if fVal, err := strconv.ParseFloat(fieldStr, 64); err == nil {
							if eVal, err2 := strconv.ParseFloat(expectedValue, 64); err2 == nil {
								return fVal >= eVal
							}
						}
						return fieldStr >= expectedValue
					case "<=":
						if fVal, err := strconv.ParseFloat(fieldStr, 64); err == nil {
							if eVal, err2 := strconv.ParseFloat(expectedValue, 64); err2 == nil {
								return fVal <= eVal
							}
						}
						return fieldStr <= expectedValue
					}
				}
			}
			break
		}
	}

	return false
}

// evaluateInCondition handles IN operator conditions like "Status IN ('Active', 'Pending')"
func (ep *ExpressionProcessor) evaluateInCondition(obj map[string]interface{}, condition string) bool {
	parts := strings.Split(condition, " IN ")
	if len(parts) != 2 {
		return false
	}

	fieldName := strings.TrimSpace(parts[0])
	valueList := strings.TrimSpace(parts[1])

	// Remove parentheses and split by comma
	valueList = strings.Trim(valueList, "()")
	values := strings.Split(valueList, ",")

	if fieldValue, exists := lookupFieldCaseInsensitive(obj, fieldName); exists {
		fieldStr := fmt.Sprintf("%v", fieldValue)

		for _, value := range values {
			cleanValue := strings.Trim(strings.TrimSpace(value), "'\"")
			if fieldStr == cleanValue {
				return true
			}
		}
	}

	return false
}

// evaluateNotInCondition handles NOT IN operator conditions like "Status NOT IN ('Inactive', 'Deleted')"
func (ep *ExpressionProcessor) evaluateNotInCondition(obj map[string]interface{}, condition string) bool {
	parts := strings.Split(condition, " NOT IN ")
	if len(parts) != 2 {
		return false
	}

	fieldName := strings.TrimSpace(parts[0])
	valueList := strings.TrimSpace(parts[1])

	// Remove parentheses and split by comma
	valueList = strings.Trim(valueList, "()")
	values := strings.Split(valueList, ",")

	if fieldValue, exists := lookupFieldCaseInsensitive(obj, fieldName); exists {
		fieldStr := fmt.Sprintf("%v", fieldValue)

		for _, value := range values {
			cleanValue := strings.Trim(strings.TrimSpace(value), "'\"")
			if fieldStr == cleanValue {
				return false // Found in list, so NOT IN is false
			}
		}
		return true // Not found in list, so NOT IN is true
	}

	return false // Field doesn't exist, consider as not matching
}

// lookupFieldCaseInsensitive returns the value for fieldName in obj. It supports nested,
// dotted relationship paths (e.g. Multibureau__r.Bureau__c) and matches each segment
// case-insensitively. This mirrors navigateNestedField so that conditional filters
// (e.g. [type__c == 'PAN'] or [Multibureau__r.Bureau__c == 'CIBIL']) resolve fields the
// same, case-insensitive and nested-aware way as simple <Object.Field> access.
func lookupFieldCaseInsensitive(obj map[string]interface{}, fieldName string) (interface{}, bool) {
	// Fast path: exact whole-key match (also covers the rare key that literally contains dots).
	if v, ok := obj[fieldName]; ok {
		return v, true
	}

	// Flat field (no nested path): single case-insensitive lookup — avoids the slice
	// allocation from strings.Split on the common per-record condition path.
	if !strings.Contains(fieldName, ".") {
		return lookupKeyCaseInsensitive(obj, fieldName)
	}

	// Nested/dotted relationship path: navigate each segment case-insensitively.
	parts := strings.Split(strings.TrimSpace(fieldName), ".")
	var current interface{} = obj
	for _, part := range parts {
		m, ok := current.(map[string]interface{})
		if !ok {
			return nil, false
		}
		val, found := lookupKeyCaseInsensitive(m, part)
		if !found {
			return nil, false
		}
		current = val
	}
	return current, true
}

// lookupKeyCaseInsensitive resolves a single map key, trying an exact match first and then a
// case-insensitive match. EqualFold performs the case-insensitive compare without allocating
// (unlike lowercasing both sides), and it is only reached when the exact match misses.
func lookupKeyCaseInsensitive(m map[string]interface{}, key string) (interface{}, bool) {
	if v, ok := m[key]; ok {
		return v, true
	}
	key = strings.TrimSpace(key)
	for k, v := range m {
		if strings.EqualFold(strings.TrimSpace(k), key) {
			return v, true
		}
	}
	return nil, false
}

// applyTypeConversion applies type conversion to a value using the existing convertToProperType system
func (ep *ExpressionProcessor) applyTypeConversion(value interface{}, targetType string) interface{} {
	// Convert value to string first (since convertToProperType expects string)
	valueStr := fmt.Sprintf("%v", value)

	switch targetType {
	case "NUMERIC":
		// Use the existing numeric conversion logic - preserve types and return null for invalid values
		if num, err := strconv.ParseFloat(valueStr, 64); err == nil {
			// Preserve original type: int stays int, float stays float
			if num == float64(int(num)) {
				return int(num)
			}
			return num
		}
		return nil // Return JSON null for invalid/empty values
	case "BOOLEAN":
		// Use the existing boolean conversion logic
		switch strings.ToLower(strings.TrimSpace(valueStr)) {
		case "true", "1", "yes", "on", "enabled":
			return true
		default:
			return false
		}
	case "STRING":
		return valueStr
	default:
		return value
	}
}

// cleanupTypeMarkers removes internal type markers from string results
func (ep *ExpressionProcessor) cleanupTypeMarkers(text string) string {
	// If wrapped in single quotes (e.g. from leaf substitution in a re-pass), unquote first
	// so we can strip internal markers; otherwise quoted '__NUMERIC_INT__:50000' would be returned as-is.
	if len(text) >= 2 && text[0] == '\'' && text[len(text)-1] == '\'' {
		inner := text[1 : len(text)-1]
		const quoteEscPlaceholder = "\uE000"
		inner = strings.ReplaceAll(inner, "\\\\", quoteEscPlaceholder)
		inner = strings.ReplaceAll(inner, "\\'", "'")
		inner = strings.ReplaceAll(inner, quoteEscPlaceholder, "\\")
		text = inner
	}
	// Remove numeric type markers
	if strings.HasPrefix(text, "__NUMERIC_INT__:") {
		return text[len("__NUMERIC_INT__:"):]
	}
	if strings.HasPrefix(text, "__NUMERIC_FLOAT__:") {
		return text[len("__NUMERIC_FLOAT__:"):]
	}

	// Remove boolean type markers
	if text == "__BOOLEAN_TRUE__" {
		return "true"
	}
	if text == "__BOOLEAN_FALSE__" {
		return "false"
	}
	if text == "__NULL_BOOLEAN__" {
		return ""
	}

	// Remove JSON parse markers
	if strings.HasPrefix(text, "__JSON_PARSE__:") {
		return text[len("__JSON_PARSE__:"):]
	}

	// Remove array parse markers
	if strings.HasPrefix(text, "__ARRAY_PARSE__:") {
		return text[len("__ARRAY_PARSE__:"):]
	}

	// Remove null numeric marker
	if text == "__NULL_NUMERIC__" {
		return ""
	}

	return text
}
