import { useEffect, useRef, useState } from 'react';
import { Link } from '@tanstack/react-router';
import { ProblemAlert } from '@/components/feedback/ProblemAlert';
import { CardDescription, CardHeader, CardTitle } from '@/components/ui/card';
import { isApiProblem } from '@/lib/api/client';
import type { ApiProblem } from '@/lib/api/problems';
import { verifyEmail } from './api';

export interface VerifyEmailScreenProps {
	token?: string;
}

type VerifyState = 'verifying' | 'verified' | 'invalid' | 'missing';

function unexpectedProblem(): ApiProblem {
	return {
		type: 'about:blank',
		title: 'Could not verify this link',
		status: 0,
		code: 'internal_error',
		detail: 'Please try again later.',
	};
}

export function VerifyEmailScreen({ token }: VerifyEmailScreenProps) {
	const started = useRef(false);
	const [state, setState] = useState<VerifyState>(
		token ? 'verifying' : 'missing',
	);
	const [problem, setProblem] = useState<ApiProblem | null>(null);

	useEffect(() => {
		if (!token || started.current) return;
		started.current = true;
		setState('verifying');

		void verifyEmail(token)
			.then(() => setState('verified'))
			.catch((error: unknown) => {
				setProblem(isApiProblem(error) ? error.problem : unexpectedProblem());
				setState('invalid');
			});
	}, [token]);

	if (state === 'verified') {
		return (
			<>
				<CardHeader>
					<CardTitle>Email verified</CardTitle>
					<CardDescription>
						Your email address is verified. You can now sign in.
					</CardDescription>
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
					<CardTitle>Verify your email</CardTitle>
					<CardDescription>
						{state === 'missing'
							? 'This verification link is missing its token'
							: 'This verification link is invalid or expired'}
					</CardDescription>
				</CardHeader>
				<div className="space-y-3 px-4">
					{state === 'invalid' ? <ProblemAlert problem={problem} /> : null}
					<Link
						to="/login"
						className="block text-sm text-muted-foreground underline underline-offset-4"
					>
						Sign in
					</Link>
					<Link
						to="/register"
						className="block text-sm text-muted-foreground underline underline-offset-4"
					>
						Create an account
					</Link>
				</div>
			</>
		);
	}

	return (
		<>
			<CardHeader>
				<CardTitle>Verifying your email</CardTitle>
				<CardDescription>This should only take a moment.</CardDescription>
			</CardHeader>
			<p className="px-4 text-sm text-muted-foreground" role="status">
				Verifying your email…
			</p>
		</>
	);
}
