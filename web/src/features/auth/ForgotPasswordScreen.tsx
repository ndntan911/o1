import { useState } from 'react';
import { Link } from '@tanstack/react-router';
import { zodResolver } from '@hookform/resolvers/zod';
import { useForm } from 'react-hook-form';
import { ProblemAlert } from '@/components/feedback/ProblemAlert';
import { Button } from '@/components/ui/button';
import { CardDescription, CardHeader, CardTitle } from '@/components/ui/card';
import { Input } from '@/components/ui/input';
import { Label } from '@/components/ui/label';
import { isApiProblem } from '@/lib/api/client';
import type { ApiProblem } from '@/lib/api/problems';
import { applyProblemToForm } from '@/lib/forms';
import { forgotPassword } from './api';
import { forgotPasswordSchema, type ForgotPasswordValues } from './schemas';

function unexpectedProblem(): ApiProblem {
	return {
		type: 'about:blank',
		title: 'Something went wrong',
		status: 0,
		code: 'internal_error',
		detail: 'Please try again.',
	};
}

export function ForgotPasswordScreen() {
	const [problem, setProblem] = useState<ApiProblem | null>(null);
	const [sent, setSent] = useState(false);
	const form = useForm<ForgotPasswordValues>({
		resolver: zodResolver(forgotPasswordSchema),
		defaultValues: { email: '' },
	});

	const onSubmit = form.handleSubmit(async (values) => {
		setProblem(null);
		try {
			await forgotPassword(values.email);
			setSent(true);
		} catch (error) {
			if (!isApiProblem(error)) {
				setProblem(unexpectedProblem());
				return;
			}
			const unmapped = applyProblemToForm(error.problem, form, {
				email: 'email',
			});
			const hasFields = Object.keys(error.problem.fields ?? {}).length > 0;
			setProblem(!hasFields || unmapped.length > 0 ? error.problem : null);
		}
	});

	if (sent) {
		return (
			<>
				<CardHeader>
					<CardTitle>Check your email</CardTitle>
					<CardDescription>
						If an account exists, we sent a reset link.
					</CardDescription>
				</CardHeader>
				<Link
					to="/login"
					className="block px-4 text-sm text-muted-foreground underline underline-offset-4"
				>
					Back to sign in
				</Link>
			</>
		);
	}

	const emailError = form.formState.errors.email;
	const isSubmitting = form.formState.isSubmitting;

	return (
		<>
			<CardHeader>
				<CardTitle>Forgot your password?</CardTitle>
				<CardDescription>
					Enter your email and we will send you a reset link.
				</CardDescription>
			</CardHeader>
			<form className="space-y-4 px-4" onSubmit={onSubmit} noValidate>
				<div className="space-y-1.5">
					<Label htmlFor="forgot-email">Email</Label>
					<Input
						id="forgot-email"
						type="email"
						autoComplete="email"
						aria-invalid={emailError ? true : undefined}
						aria-describedby={emailError ? 'forgot-email-error' : undefined}
						{...form.register('email')}
					/>
					{emailError ? (
						<p id="forgot-email-error" className="text-sm text-destructive">
							{emailError.message}
						</p>
					) : null}
				</div>

				<ProblemAlert problem={problem} />

				<Button type="submit" className="w-full" disabled={isSubmitting}>
					{isSubmitting ? 'Sending…' : 'Send reset link'}
				</Button>

				<Link
					to="/login"
					className="text-sm text-muted-foreground underline underline-offset-4"
				>
					Back to sign in
				</Link>
			</form>
		</>
	);
}
