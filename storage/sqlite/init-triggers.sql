create trigger if not exists delete_blob_tags_before_blob_deleted
before delete on blob
begin
    delete from tag where blob_name=old.name;
end;

create trigger if not exists after_insert_blob
after insert on blob
begin
    update blob_meta set value=value+length(cast(new.data as blob)) where key='size';
    delete from blob where rowid in (select blob_rowid from deletable_blob);
end;

create trigger if not exists after_update_blob
after update of data on blob
begin
    update blob_meta set value=value+length(cast(new.data as blob))-length(cast(old.data as blob)) where key='size';
    delete from blob where rowid in (select blob_rowid from deletable_blob);
end;

create trigger if not exists after_delete_blob
after delete on blob
begin
    update blob_meta set value=value-length(cast(old.data as blob)) where key='size';
end;
