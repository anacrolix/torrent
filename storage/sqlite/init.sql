-- We have to opt into this before creating any tables, or before a vacuum to enable it. It means we
-- can trim the database file size with partial vacuums without having to do a full vacuum, which
-- locks everything.
pragma auto_vacuum=incremental;

create table if not exists blob (
    name text,
    last_used timestamp default (datetime('now')),
    data blob,
    primary key (name)
);

create table if not exists blob_meta (
    key text primary key,
    value
);

create index if not exists blob_last_used on blob(last_used);

-- While sqlite *seems* to be faster to get sum(length(data)) instead of
-- sum(length(data)), it may still require a large table scan at start-up or with a
-- cold-cache. With this we can be assured that it doesn't.
insert or ignore into blob_meta values ('size', 0);

create table if not exists setting (
    name primary key on conflict replace,
    value
);

create table if not exists tag (
    blob_name references blob(name),
    tag_name,
    value,
    primary key (blob_name, tag_name)
);

create view if not exists deletable_blob as
with recursive excess (
    usage_with,
    last_used,
    blob_rowid,
    data_length
) as (
    select *
    from (
        select
            (select value from blob_meta where key='size') as usage_with,
            last_used,
            rowid,
            length(data)
        from blob order by last_used, rowid limit 1
    )
    where usage_with > (select value from setting where name='capacity')
    union all
    select
        usage_with-data_length as new_usage_with,
        blob.last_used,
        blob.rowid,
        length(data)
    from excess join blob
    on blob.rowid=(select rowid from blob where (last_used, rowid) > (excess.last_used, blob_rowid))
    where new_usage_with > (select value from setting where name='capacity')
)
select * from excess;
