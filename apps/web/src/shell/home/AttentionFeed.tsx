import { Link } from "react-router-dom";
import { copy } from "../copy";
import { ProblemMessage } from "../errors";
import { MY_WORK_PATH } from "../inbox/inbox";
import pageStyles from "../pages/pages.module.css";
import { Alert, Badge, Button, Card, EmptyState, Skeleton } from "../ui";
import { FEED_VISIBLE_LIMIT, type FeedItem } from "./feedSources";
import { useAttentionFeed } from "./useAttentionFeed";

function Row({ item }: { item: FeedItem }) {
  return (
    <li className={pageStyles.link}>
      <span>
        {item.kind}: {item.number}. {item.reason}.{" "}
        {item.overdue ? <Badge tone="danger">{copy.attention.overdue}</Badge> : null}{" "}
        {item.dueDate ? copy.attention.dueOn(item.dueDate.slice(0, 10)) : copy.attention.noDueDate}
      </span>
      <Link to={item.link} aria-label={copy.attention.reviewLabel(item.kind, item.number)}>
        {copy.attention.review}
      </Link>
    </li>
  );
}

export function AttentionFeed() {
  const feed = useAttentionFeed();
  if (!feed.enabled) return null;
  const waiting = feed.loading && feed.items.length === 0;
  const visible = feed.items.slice(0, FEED_VISIBLE_LIMIT);
  const retry = <Button onClick={feed.reload}>{copy.attention.retry}</Button>;

  return (
    <Card title={copy.attention.title} headingLevel={2}>
      <p role="status">{waiting || feed.items.length === 0 ? "" : copy.attention.summary(feed.items.length, feed.hasMore)}</p>
      {feed.needsEntity ? (
        <Alert tone="danger" live="alert" action={retry}>
          {copy.myWork.needEntity}
        </Alert>
      ) : feed.soleInboxProblem ? (
        <Alert tone="danger" live="alert" action={retry}>
          <ProblemMessage problem={feed.soleInboxProblem} fallback={copy.myWork.errorTitle} />
        </Alert>
      ) : feed.unavailable.length > 0 ? (
        <Alert tone="warning" live="status" action={retry}>
          {copy.attention.partial(feed.unavailable.join(", "))}
        </Alert>
      ) : null}
      {waiting ? (
        <div aria-busy="true">
          <span className="visually-hidden">{copy.attention.loading}</span>
          <Skeleton width="3/4" />
        </div>
      ) : feed.items.length === 0 ? (
        feed.unavailable.length === 0 && !feed.needsEntity ? (
          <EmptyState compact title={copy.attention.emptyTitle} description={copy.attention.emptyDescription} />
        ) : null
      ) : (
        <>
          <ul className={pageStyles.links} aria-label={copy.attention.title}>
            {visible.map((item) => (
              <Row key={item.key} item={item} />
            ))}
          </ul>
          {feed.items.length > FEED_VISIBLE_LIMIT || feed.hasMore ? (
            <p>
              <Link to={MY_WORK_PATH} className={pageStyles.seeAll}>
                {copy.attention.seeAll}
              </Link>
            </p>
          ) : null}
        </>
      )}
    </Card>
  );
}
