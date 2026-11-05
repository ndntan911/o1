import { useState } from 'react';
import { zodResolver } from '@hookform/resolvers/zod';
import { useForm } from 'react-hook-form';
import { z } from 'zod';
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
import { isApiProblem } from '@/lib/api/client';
import type { ApiProblem } from '@/lib/api/problems';
import { applyProblemToForm } from '@/lib/forms';
import { useCreateWorkspace } from './hooks';
import { workspaceNameSchema } from './schemas';

const createWorkspaceSchema = z.object({ name: workspaceNameSchema });
type CreateWorkspaceValues = z.infer<typeof createWorkspaceSchema>;

export interface WorkspaceCreateDialogProps {
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

export function WorkspaceCreateDialog({
	open,
	onOpenChange,
}: WorkspaceCreateDialogProps) {
	const mutation = useCreateWorkspace();
	const [problem, setProblem] = useState<ApiProblem | null>(null);
	const form = useForm<CreateWorkspaceValues>({
		resolver: zodResolver(createWorkspaceSchema),
		defaultValues: { name: '' },
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
			await mutation.mutateAsync(values.name);
			handleOpenChange(false);
		} catch (error) {
			if (!isApiProblem(error)) {
				setProblem(unexpectedProblem());
				return;
			}
			const unmapped = applyProblemToForm(error.problem, form, {
				name: 'name',
			});
			const hasFields = Object.keys(error.problem.fields ?? {}).length > 0;
			setProblem(!hasFields || unmapped.length > 0 ? error.problem : null);
		}
	});

	const nameError = form.formState.errors.name;
	const pending = mutation.isPending || form.formState.isSubmitting;

	return (
		<Dialog open={open} onOpenChange={handleOpenChange}>
			<DialogContent>
				<DialogHeader>
					<DialogTitle>New workspace</DialogTitle>
					<DialogDescription>
						Workspaces group monitors and the people who can manage them.
					</DialogDescription>
				</DialogHeader>
				<form className="space-y-4" onSubmit={onSubmit} noValidate>
					<div className="space-y-1.5">
						<Label htmlFor="workspace-name">Name</Label>
						<Input
							id="workspace-name"
							autoComplete="off"
							aria-invalid={nameError ? true : undefined}
							aria-describedby={nameError ? 'workspace-name-error' : undefined}
							{...form.register('name')}
						/>
						{nameError ? (
							<p id="workspace-name-error" className="text-sm text-destructive">
								{nameError.message}
							</p>
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
							{pending ? 'Creating…' : 'Create workspace'}
						</Button>
					</DialogFooter>
				</form>
			</DialogContent>
		</Dialog>
	);
}
