import { createFileRoute } from '@tanstack/react-router';
import { PlanReview } from '../pages/app';

export const Route = createFileRoute('/app/plan/$id')({
  component: () => <PlanReview id={Route.useParams().id} />,
});
