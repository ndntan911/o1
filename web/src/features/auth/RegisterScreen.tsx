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
import { register, resendVerification } from './api';
import { registerSchema, type RegisterValues } from './schemas';

function unexpectedProblem(): ApiProblem {
	return {
		type: 'about:blank',
		title: 'Something went wrong',
		status: 0,
		code: 'internal_error',
		detail: 'Please try again.',
	};
}

export function RegisterScreen() {
	const [problem, setProblem] = useState<ApiProblem | null>(null);
	const [submittedEmail, setSubmittedEmail] = useState<string | null>(null);
	const [resendState, setResendState] = useState<
		'idle' | 'sending' | 'sent' | 'error'
	>('idle');
	const form = useForm<RegisterValues>({
		resolver: zodResolver(registerSchema),
		defaultValues: { email: '', password: '', confirmPassword: '' },
	});

	const onSubmit = form.handleSubmit(async (values) => {
		setProblem(null);
		try {
			const response = await register({
				email: values.email,
				password: values.password,
			});
			setSubmittedEmail(response.email || values.email);
		} catch (error) {
			if (!isApiProblem(error)) {
				setProblem(unexpectedProblem());
				return;
			}
			const unmapped = applyProblemToForm(error.problem, form, {
				email: 'email',
				password: 'password',
			});
			const hasFields = Object.keys(error.problem.fields ?? {}).length > 0;
			setProblem(!hasFields || unmapped.length > 0 ? error.problem : null);
		}
	});

	async function handleResend() {
		if (!submittedEmail) return;
		setResendState('sending');
		try {
			await resendVerification(submittedEmail);
			setResendState('sent');
		} catch {
			setResendState('error');
		}
	}

	if (submittedEmail) {
		return (
			<>
				<CardHeader>
					<CardTitle>Check your email</CardTitle>
					<CardDescription>
						We sent a verification link to {submittedEmail}.
					</CardDescription>
				</CardHeader>
				<div className="space-y-4 px-4">
					<p className="text-sm text-muted-foreground">
						Click the link in the email to verify your account, then sign in.
					</p>
					<Button
						type="button"
						variant="outline"
						onClick={() => void handleResend()}
						disabled={resendState === 'sending'}
					>
						{resendState === 'sending'
							? 'Sending…'
							: 'Resend verification email'}
					</Button>
					{resendState === 'sent' ? (
						<p className="text-sm text-muted-foreground" role="status">
							Verification email sent.
						</p>
					) : null}
					{resendState === 'error' ? (
						<p className="text-sm text-destructive" role="status">
							Could not send the verification email. Try again.
						</p>
					) : null}
					<Link
						to="/login"
						className="text-sm text-muted-foreground underline underline-offset-4"
					>
						Sign in
					</Link>
				</div>
			</>
		);
	}

	const emailError = form.formState.errors.email;
	const passwordError = form.formState.errors.password;
	const confirmPasswordError = form.formState.errors.confirmPassword;
	const isSubmitting = form.formState.isSubmitting;

	return (
		<>
			<CardHeader>
				<CardTitle>Create your account</CardTitle>
				<CardDescription>
					Start monitoring your services in a few minutes.
				</CardDescription>
			</CardHeader>
			<form className="space-y-4 px-4" onSubmit={onSubmit} noValidate>
				<div className="space-y-1.5">
					<Label htmlFor="register-email">Email</Label>
					<Input
						id="register-email"
						type="email"
						autoComplete="email"
						aria-invalid={emailError ? true : undefined}
						aria-describedby={emailError ? 'register-email-error' : undefined}
						{...form.register('email')}
					/>
					{emailError ? (
						<p id="register-email-error" className="text-sm text-destructive">
							{emailError.message}
						</p>
					) : null}
				</div>

				<div className="space-y-1.5">
					<Label htmlFor="register-password">Password</Label>
					<Input
						id="register-password"
						type="password"
						autoComplete="new-password"
						aria-invalid={passwordError ? true : undefined}
						aria-describedby={
							passwordError ? 'register-password-error' : undefined
						}
						{...form.register('password')}
					/>
					{passwordError ? (
						<p
							id="register-password-error"
							className="text-sm text-destructive"
						>
							{passwordError.message}
						</p>
					) : (
						<p className="text-xs text-muted-foreground">
							Use at least 12 characters.
						</p>
					)}
				</div>

				<div className="space-y-1.5">
					<Label htmlFor="register-confirm-password">Confirm password</Label>
					<Input
						id="register-confirm-password"
						type="password"
						autoComplete="new-password"
						aria-invalid={confirmPasswordError ? true : undefined}
						aria-describedby={
							confirmPasswordError
								? 'register-confirm-password-error'
								: undefined
						}
						{...form.register('confirmPassword')}
					/>
					{confirmPasswordError ? (
						<p
							id="register-confirm-password-error"
							className="text-sm text-destructive"
						>
							{confirmPasswordError.message}
						</p>
					) : null}
				</div>

				<ProblemAlert problem={problem} />

				<Button type="submit" className="w-full" disabled={isSubmitting}>
					{isSubmitting ? 'Creating account…' : 'Create account'}
				</Button>

				<Link
					to="/login"
					className="text-sm text-muted-foreground underline underline-offset-4"
				>
					Already have an account? Sign in
				</Link>
			</form>
		</>
	);
}
