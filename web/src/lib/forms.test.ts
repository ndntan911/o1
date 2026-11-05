import { describe, expect, test, vi } from 'vitest';
import { renderHook } from '@testing-library/react';
import { useForm } from 'react-hook-form';
import { applyProblemToForm } from './forms';
import type { ApiProblem } from './api/problems';

interface FormValues {
	name: string;
	url: string;
}

const problem: ApiProblem = {
	type: 'about:blank',
	title: 'Validation Error',
	status: 400,
	code: 'validation_error',
	fields: {
		name: 'Name is required',
		unmapped: 'This field has no form control',
	},
};

describe('applyProblemToForm', () => {
	test('sets mapped errors and returns unmapped messages', () => {
		const { result } = renderHook(() => useForm<FormValues>());
		const form = result.current;
		const setError = vi.spyOn(form, 'setError');

		const messages = applyProblemToForm(problem, form, { name: 'name' });

		expect(setError).toHaveBeenCalledTimes(1);
		expect(setError).toHaveBeenCalledWith(
			'name',
			expect.objectContaining({ message: 'Name is required' }),
		);
		expect(messages).toEqual(['This field has no form control']);
	});
});
