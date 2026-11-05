import { useState } from 'react';
import { Link, useNavigate } from '@tanstack/react-router';
import { useQueryClient } from '@tanstack/react-query';
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
import { queryKeys } from '@/lib/query/keys';
import { login, resendVerification } from './api';
import { safeRedirect } from './guards';
import { loginSchema, type LoginValues } from './schemas';

export interface LoginScreenProps {
	redirectTo?: string;
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

export function LoginScreen({ redirectTo }: LoginScreenProps) {
	const navigate = useNavigate();
	const queryClient = useQueryClient();
	const [problem, setProblem] = useState<ApiProblem | null>(null);
	const [resendState, setResendState] = useState<
		'idle' | 'sending' | 'sent' | 'error'
	>('idle');
	const form = useForm<LoginValues>({
		resolver: zodResolver(loginSchema),
		defaultValues: { email: '', password: '' },
	});

	const onSubmit = form.handleSubmit(async (values) => {
		setProblem(null);
		setResendState('idle');
		try {
			await login(values);
			await queryClient.invalidateQueries({ queryKey: queryKeys.me });
			await navigate({ to: safeRedirect(redirectTo) });
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
		const email = form.getValues('email');
		setResendState('sending');
		try {
			await resendVerification(email);
			setResendState('sent');
		} catch {
			setResendState('error');
		}
	}

	const emailError = form.formState.errors.email;
	const passwordError = form.formState.errors.password;
	const isSubmitting = form.formState.isSubmitting;

	return (
		<>
			<CardHeader>
				<CardTitle>Sign in</CardTitle>
				<CardDescription>
					Sign in to manage your uptime monitors.
				</CardDescription>
			</CardHeader>
			<form className="space-y-4 px-4" onSubmit={onSubmit} noValidate>
				<div className="space-y-1.5">
					<Label htmlFor="login-email">Email</Label>
					<Input
						id="login-email"
						type="email"
						autoComplete="email"
						aria-invalid={emailError ? true : undefined}
						aria-describedby={emailError ? 'login-email-error' : undefined}
						{...form.register('email')}
					/>
					{emailError ? (
						<p id="login-email-error" className="text-sm text-destructive">
							{emailError.message}
						</p>
					) : null}
				</div>

				<div className="space-y-1.5">
					<Label htmlFor="login-password">Password</Label>
					<Input
						id="login-password"
						type="password"
						autoComplete="current-password"
						aria-invalid={passwordError ? true : undefined}
						aria-describedby={
							passwordError ? 'login-password-error' : undefined
						}
						{...form.register('password')}
					/>
					{passwordError ? (
						<p id="login-password-error" className="text-sm text-destructive">
							{passwordError.message}
						</p>
					) : null}
				</div>

				<ProblemAlert problem={problem} />

				{problem?.code === 'email_not_verified' ? (
					<div className="space-y-2 rounded-lg border p-3">
						<p className="text-sm text-muted-foreground">
							Verify your email before signing in. We can send another
							verification link.
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
					</div>
				) : null}

				<Button type="submit" className="w-full" disabled={isSubmitting}>
					{isSubmitting ? 'Signing in…' : 'Sign in'}
				</Button>

				<div className="flex flex-col gap-2 text-sm text-muted-foreground">
					<Link to="/register" className="underline underline-offset-4">
						Create an account
					</Link>
					<Link to="/forgot-password" className="underline underline-offset-4">
						Forgot your password?
					</Link>
				</div>
			</form>
		</>
	);
}
