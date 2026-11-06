import { isApiProblem } from './client';
import type { ApiProblem } from './problems';

/**
 * Convert any mutation/query error into an `ApiProblem` suitable for a
 * `ProblemAlert`. Network failures and other unknown errors are reported as an
 * `internal_error` so they are never silently dropped.
 */
export function problemFromError(error: unknown): ApiProblem {
	if (isApiProblem(error)) return error.problem;

	return {
		type: 'about:blank',
		title: 'Something went wrong',
		status: 0,
		code: 'internal_error',
		detail: 'Please try again.',
	};
}
