package audit

// Both reservation and budget-aware routing use this selector. r supplies the
// canonical model and credential_id; $3 is the request's pricing timestamp.
// Resolve the latest version before checking active so a reset cannot revive an
// older override. Literal substring matching does not interpret SQL wildcards.
const matchingPriceSQL = `
SELECT id,model,rates,source,effective_at,credential_id
FROM (
 SELECT DISTINCT ON(p.model,p.credential_id) p.*
 FROM model_prices p
 WHERE p.credential_id IN ('',r.credential_id) AND p.effective_at<=$3
   AND p.model<>'' AND strpos(lower(r.model),lower(p.model))>0
 ORDER BY p.model,p.credential_id,p.effective_at DESC,p.id DESC
) latest
WHERE active
ORDER BY (lower(model)=lower(r.model)) DESC,char_length(model) DESC,
 (credential_id<>'') DESC,effective_at DESC,id DESC
LIMIT 1`
