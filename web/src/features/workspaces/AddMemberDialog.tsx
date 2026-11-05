import { useState } from 'react';
import { zodResolver } from '@hookform/resolvers/zod';
import { Controller, useForm } from 'react-hook-form';
import { ProblemAlert } from '@/components/feedback/ProblemAlert';
import { Button } from '@/components/ui/button';
import {
	Dialog,
	DialogContent,
	DialogDescription,
	DialogFooter,
	DialogHeader,
	DialogTitle,
} from '@/components/ui/dialog';
import { Input } from '@/components/ui/input';
import { Label } from '@/components/ui/label';
import {
	Select,
	SelectContent,
	SelectItem,
	SelectTrigger,
	SelectValue,
} from '@/components/ui/select';
import { isApiProblem } from '@/lib/api/client';
import type { ApiProblem } from '@/lib/api/problems';
import { applyProblemToForm } from '@/lib/forms';
import { useAddMember } from './hooks';
import { addMemberSchema, type AddMemberValues } from './schemas';

export type AssignableRole = AddMemberValues['role'];

export interface AddMemberDialogProps {
	workspaceId: string;
	roles: AssignableRole[];
	open: boolean;
	onOpenChange: (open: boolean) => void;
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

function withActionableDetail(problem: ApiProblem): ApiProblem {
	if (problem.code === 'not_found') {
		return { ...problem, detail: 'No account with that email' };
	}
	if (problem.code === 'forbidden') {
		return {
			...problem,
			detail: 'You do not have permission to add members.',
		};
	}
	return problem;
}

export function AddMemberDialog({
	workspaceId,
	roles,
	open,
	onOpenChange,
}: AddMemberDialogProps) {
	const mutation = useAddMember(workspaceId);
	const [problem, setProblem] = useState<ApiProblem | null>(null);
	const defaultRole = roles[0] ?? 'viewer';
	const form = useForm<AddMemberValues>({
		resolver: zodResolver(addMemberSchema),
		defaultValues: { email: '', role: defaultRole },
	});

	function handleOpenChange(nextOpen: boolean) {
		if (!nextOpen) {
			form.reset();
			setProblem(null);
		}
		onOpenChange(nextOpen);
	}

	const onSubmit = form.handleSubmit(async (values) => {
		setProblem(null);
		try {
			await mutation.mutateAsync(values);
			handleOpenChange(false);
		} catch (error) {
			if (!isApiProblem(error)) {
				setProblem(unexpectedProblem());
				return;
			}
			const unmapped = applyProblemToForm(error.problem, form, {
				email: 'email',
				role: 'role',
			});
			const actionable = withActionableDetail(error.problem);
			const hasFields = Object.keys(error.problem.fields ?? {}).length > 0;
			setProblem(
				actionable !== error.problem || !hasFields || unmapped.length > 0
					? actionable
					: null,
			);
		}
	});

	const emailError = form.formState.errors.email;
	const roleError = form.formState.errors.role;
	const pending = mutation.isPending || form.formState.isSubmitting;

	return (
		<Dialog open={open} onOpenChange={handleOpenChange}>
			<DialogContent>
				<DialogHeader>
					<DialogTitle>Add member</DialogTitle>
					<DialogDescription>
						The person must already have an account.
					</DialogDescription>
				</DialogHeader>

				<form className="space-y-4" onSubmit={onSubmit} noValidate>
					<div className="space-y-1.5">
						<Label htmlFor="add-member-email">Email</Label>
						<Input
							id="add-member-email"
							type="email"
							autoComplete="email"
							aria-invalid={emailError ? true : undefined}
							aria-describedby={
								emailError ? 'add-member-email-error' : undefined
							}
							{...form.register('email')}
						/>
						{emailError ? (
							<p
								id="add-member-email-error"
								className="text-sm text-destructive"
							>
								{emailError.message}
							</p>
						) : null}
					</div>

					<div className="space-y-1.5">
						<Label htmlFor="add-member-role">Role</Label>
						<Controller
							control={form.control}
							name="role"
							render={({ field }) => (
								<Select value={field.value} onValueChange={field.onChange}>
									<SelectTrigger
										id="add-member-role"
										className="w-full"
										aria-label="Role"
										aria-invalid={roleError ? true : undefined}
									>
										<SelectValue placeholder="Select a role" />
									</SelectTrigger>
									<SelectContent>
										{roles.map((role) => (
											<SelectItem key={role} value={role}>
												{role}
											</SelectItem>
										))}
									</SelectContent>
								</Select>
							)}
						/>
						{roleError ? (
							<p className="text-sm text-destructive">{roleError.message}</p>
						) : null}
					</div>

					<ProblemAlert problem={problem} />

					<DialogFooter>
						<Button
							type="button"
							variant="outline"
							onClick={() => handleOpenChange(false)}
							disabled={pending}
						>
							Cancel
						</Button>
						<Button type="submit" disabled={pending}>
							{pending ? 'Adding…' : 'Add member'}
						</Button>
					</DialogFooter>
				</form>
			</DialogContent>
		</Dialog>
	);
}
