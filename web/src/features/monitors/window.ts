import type { ResultsWindow } from '@/lib/api/types';

export const RESULTS_WINDOWS = [
	'1h',
	'6h',
	'24h',
	'7d',
] as const satisfies readonly ResultsWindow[];

export const DEFAULT_RESULTS_WINDOW: ResultsWindow = '24h';

export function isResultsWindow(value: unknown): value is ResultsWindow {
	return (
		typeof value === 'string' &&
		(RESULTS_WINDOWS as readonly string[]).includes(value)
	);
}

export function parseMonitorWindow(value: unknown): ResultsWindow {
	return isResultsWindow(value) ? value : DEFAULT_RESULTS_WINDOW;
}
