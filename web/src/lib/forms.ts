import type { FieldValues, Path, UseFormReturn } from 'react-hook-form';
import type { ApiProblem } from './api/problems';

export function applyProblemToForm<T extends FieldValues>(
	problem: ApiProblem,
	form: UseFormReturn<T>,
	fieldMap: Record<string, keyof T>,
): string[] {
	const unmappedMessages: string[] = [];

	for (const [key, message] of Object.entries(problem.fields ?? {})) {
		const field = fieldMap[key];
		if (field === undefined) {
			unmappedMessages.push(message);
			continue;
		}
		form.setError(field as Path<T>, { type: 'server', message });
	}

	return unmappedMessages;
}
