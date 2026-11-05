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
import { resetPassword } from './api';
import { resetPasswordSchema, type ResetPasswordValues } from './schemas';

export interface ResetPasswordScreenProps {
	token?: string;
}

function unexpectedProblem(): ApiProblem {
	return {
		type: 'about:blank',
		title: 'Something went wrong',
		status: 0,
		code: 'internal_error',
		detail: 'Please try again.',
	};
}

export function ResetPasswordScreen({ token }: ResetPasswordScreenProps) {
	const [problem, setProblem] = useState<ApiProblem | null>(null);
	const [state, setState] = useState<
		'form' | 'success' | 'invalid' | 'missing'
	>(token ? 'form' : 'missing');
	const form = useForm<ResetPasswordValues>({
		resolver: zodResolver(resetPasswordSchema),
		defaultValues: { password: '', confirmPassword: '' },
	});

	const onSubmit = form.handleSubmit(async (values) => {
		if (!token) return;
		setProblem(null);
		try {
			await resetPassword({
				token,
				new_password: values.password,
			});
			setState('success');
		} catch (error) {
			if (!isApiProblem(error)) {
				setProblem(unexpectedProblem());
				return;
			}
			if (error.problem.code === 'token_invalid') {
				setProblem(error.problem);
				setState('invalid');
				return;
			}
			const unmapped = applyProblemToForm(error.problem, form, {
				new_password: 'password',
			});
			const hasFields = Object.keys(error.problem.fields ?? {}).length > 0;
			setProblem(!hasFields || unmapped.length > 0 ? error.problem : null);
		}
	});

	if (state === 'success') {
		return (
			<>
				<CardHeader>
					<CardTitle>Password reset</CardTitle>
					<CardDescription>Your password has been reset.</CardDescription>
				</CardHeader>
				<Link
					to="/login"
					className="block px-4 text-sm text-muted-foreground underline underline-offset-4"
				>
					Sign in
				</Link>
			</>
		);
	}

	if (state === 'invalid' || state === 'missing') {
		return (
			<>
				<CardHeader>
					<CardTitle>Reset your password</CardTitle>
					<CardDescription>
						{state === 'missing'
							? 'This reset link is missing its token'
							: 'This reset link is invalid or expired'}
					</CardDescription>
				</CardHeader>
				<div className="space-y-3 px-4">
					{state === 'invalid' ? <ProblemAlert problem={problem} /> : null}
					<Link
						to="/forgot-password"
						className="block text-sm text-muted-foreground underline underline-offset-4"
					>
						Request a new reset link
					</Link>
					<Link
						to="/login"
						className="block text-sm text-muted-foreground underline underline-offset-4"
					>
						Back to sign in
					</Link>
				</div>
			</>
		);
	}

	const passwordError = form.formState.errors.password;
	const confirmPasswordError = form.formState.errors.confirmPassword;
	const isSubmitting = form.formState.isSubmitting;

	return (
		<>
			<CardHeader>
				<CardTitle>Reset your password</CardTitle>
				<CardDescription>
					Choose a new password for your account.
				</CardDescription>
			</CardHeader>
			<form className="space-y-4 px-4" onSubmit={onSubmit} noValidate>
				<div className="space-y-1.5">
					<Label htmlFor="reset-password">Password</Label>
					<Input
						id="reset-password"
						type="password"
						autoComplete="new-password"
						aria-invalid={passwordError ? true : undefined}
						aria-describedby={
							passwordError ? 'reset-password-error' : undefined
						}
						{...form.register('password')}
					/>
					{passwordError ? (
						<p id="reset-password-error" className="text-sm text-destructive">
							{passwordError.message}
						</p>
					) : (
						<p className="text-xs text-muted-foreground">
							Use at least 12 characters.
						</p>
					)}
				</div>

				<div className="space-y-1.5">
					<Label htmlFor="reset-confirm-password">Confirm password</Label>
					<Input
						id="reset-confirm-password"
						type="password"
						autoComplete="new-password"
						aria-invalid={confirmPasswordError ? true : undefined}
						aria-describedby={
							confirmPasswordError ? 'reset-confirm-password-error' : undefined
						}
						{...form.register('confirmPassword')}
					/>
					{confirmPasswordError ? (
						<p
							id="reset-confirm-password-error"
							className="text-sm text-destructive"
						>
							{confirmPasswordError.message}
						</p>
					) : null}
				</div>

				<ProblemAlert problem={problem} />

				<Button type="submit" className="w-full" disabled={isSubmitting}>
					{isSubmitting ? 'Resetting…' : 'Reset password'}
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
