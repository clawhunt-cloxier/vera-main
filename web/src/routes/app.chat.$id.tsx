import { createFileRoute } from '@tanstack/react-router';
import { ConversationPage } from '../pages/conversations';

export const Route = createFileRoute('/app/chat/$id')({
  component: ChatRoute,
});

function ChatRoute() {
  const { id } = Route.useParams();
  return <ConversationPage id={id} />;
}
