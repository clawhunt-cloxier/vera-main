import { createFileRoute } from '@tanstack/react-router';
import { PlanReview } from '../pages/app';

export const Route = createFileRoute('/app/plan/$id')({
  component: function PlanRoute() {
    const { id } = Route.useParams();
    return <PlanReview key={id} id={id} />;
  },
});
