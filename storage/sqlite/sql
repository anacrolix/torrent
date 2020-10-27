pragma auto_vacuum=incremental;
create table if not exists blob(
	name text,
	last_used timestamp default (datetime('now')),
	data blob,
	primary key (name)
);

create view if not exists deletable_blob as
with recursive excess_blob(
	usage_with,
	last_used,
	blob_rowid,
	data_length
) as (
	select * from (select (select sum(length(data)) from blob) as usage_with, last_used, rowid, length(data) from blob order by last_used, rowid limit 1)
		where usage_with >= (select value from setting where name='capacity')
	union all
	select usage_with-data_length, blob.last_used, blob.rowid, length(data) from excess_blob join blob
		on blob.rowid=(select rowid from blob where (last_used, rowid) > (excess.last_used, blob_rowid))
	where usage_with >= (select value from setting where name='capacity')
) select * from excess;

CREATE TRIGGER if not exists trim_blobs_to_capacity_after_update after update on blob begin
	delete from blob where rowid in (select blob_rowid from deletable_blob);
end;
CREATE TRIGGER if not exists trim_blobs_to_capacity_after_insert after insert on blob begin
	delete from blob where rowid in (select blob_rowid from deletable_blob);
end;
